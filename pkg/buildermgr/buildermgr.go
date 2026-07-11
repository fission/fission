// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/svcinfo"
	"github.com/fission/fission/pkg/tenant"
	"github.com/fission/fission/pkg/utils/crmanager"
	"github.com/fission/fission/pkg/utils/httpserver"
	fissionmetrics "github.com/fission/fission/pkg/utils/metrics"
)

const (
	// leaderElectionID is the name of the Lease the builder manager contends
	// for. Kept identical to the client-go lease name used before the
	// controller-runtime migration so there is no orphaned lease across the
	// upgrade.
	leaderElectionID = "fission-buildermgr"

	// defaultPackageBuildConcurrency bounds how many package builds run at once.
	// Each build holds a reconcile worker for the duration of the fetch + build
	// + upload, so this replaces the old unbounded per-package build goroutines.
	// Overridable via BUILDERMGR_PACKAGE_CONCURRENCY.
	defaultPackageBuildConcurrency = 5
)

// Start runs the builder manager under a controller-runtime Manager. The
// Manager owns leader election, health/readiness probes, the metrics server and
// graceful shutdown, and hosts the Environment and Package reconcilers. The
// legacy GroupManager argument is unused now that the Manager owns the
// lifecycle.
func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, _ *errgroup.Group, storageSvcUrl string) error {
	bmLogger := logger.WithName("builder_manager")

	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return fmt.Errorf("failed to get fission client: %w", err)
	}
	kubernetesClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return fmt.Errorf("failed to get kubernetes client: %w", err)
	}
	restConfig, err := clientGen.GetRestConfig()
	if err != nil {
		return fmt.Errorf("failed to get rest config: %w", err)
	}

	if err := crd.WaitForFunctionCRDs(ctx, bmLogger, fissionClient); err != nil {
		return fmt.Errorf("error waiting for CRDs: %w", err)
	}

	fConfig, err := fetcherConfig.MakeFetcherConfig("/packages")
	if err != nil {
		return fmt.Errorf("error making fetcher config: %w", err)
	}

	podSpecPatch, err := util.GetSpecFromConfigMap(fv1.BuilderPodSpecPath)
	if err != nil && !os.IsNotExist(err) {
		bmLogger.Error(err, "error reading data for pod spec patch", "path", fv1.BuilderPodSpecPath)
	}

	leaderElectionEnabled, _ := strconv.ParseBool(os.Getenv("LEADER_ELECTION_ENABLED"))

	// Fission's custom collectors register into controller-runtime's global
	// metrics registry; the Manager's metrics server then serves them on
	// METRICS_ADDR (preserving the existing :8080 scrape). AlreadyRegistered is
	// benign — it only happens when another Fission service shares the process
	// (the e2e harness) and has already registered the same collectors.
	var alreadyRegistered prometheus.AlreadyRegisteredError
	if err := ctrlmetrics.Registry.Register(fissionmetrics.Registry); err != nil && !errors.As(err, &alreadyRegistered) {
		bmLogger.Error(err, "failed to register fission metrics collectors")
	}

	metricsBind := httpserver.BindAddrFromEnv("METRICS_ADDR", svcinfo.PortMetrics)
	healthBind := httpserver.BindAddrFromEnv("HEALTH_PROBE_ADDR", svcinfo.PortHealthProbe)
	if ephemeral, _ := strconv.ParseBool(os.Getenv("FISSION_TEST_EPHEMERAL_SERVERS")); ephemeral {
		// The e2e framework runs every Fission service in one process sharing
		// METRICS_ADDR, which is fine for the fail-soft ServeMetrics servers but
		// clashes with the Manager's hard-binding servers. Bind ephemeral ports
		// (OS-assigned, race-free) in that mode. Never set in production.
		metricsBind, healthBind = ":0", ":0"
	}

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme: scheme.Scheme,
		// Scope the shared cache to the Fission-watched namespaces. The
		// Environment/Package reconcilers read through this cache, and the
		// buildermgr's RBAC is per-namespace Roles (not a ClusterRole) — a
		// cluster-wide cache's list/watch is forbidden, so its sync times out and
		// the manager exits. This reproduces the per-namespace informers the
		// pre-reconciler watchers used. See crmanager.FissionCacheOptions.
		Cache:                         crmanager.FissionCacheOptions(),
		Metrics:                       metricsserver.Options{BindAddress: metricsBind},
		HealthProbeBindAddress:        healthBind,
		LeaderElection:                leaderElectionEnabled,
		LeaderElectionID:              leaderElectionID,
		LeaderElectionReleaseOnCancel: true,
		Logger:                        bmLogger,
	})
	if err != nil {
		return fmt.Errorf("unable to set up builder manager: %w", err)
	}

	envReconciler := makeEnvironmentReconciler(bmLogger, mgr.GetClient(), kubernetesClient, fConfig, podSpecPatch)
	if err := controller.RegisterTenantScoped(mgr, &fv1.Environment{}, envReconciler, "buildermgr-environment"); err != nil {
		return fmt.Errorf("unable to register environment reconciler: %w", err)
	}

	registryCfg, err := loadPackageRegistryConfig(bmLogger)
	if err != nil {
		return fmt.Errorf("error loading package registry config: %w", err)
	}
	if registryCfg.enabled {
		bmLogger.Info("OCI package producer enabled (RFC-0012)",
			"repositoryPrefix", registryCfg.repositoryPrefix,
			"fallbackToStorage", registryCfg.fallbackToStorage,
			"pushSecret", registryCfg.pushSecret != "",
			"pullSecret", registryCfg.pullSecret != "")
	}
	pkgReconciler := makePackageReconciler(bmLogger, mgr.GetClient(), fissionClient, kubernetesClient, storageSvcUrl, registryCfg)
	if err := controller.RegisterTenantScopedWithPredicates(mgr, &fv1.Package{}, pkgReconciler, "buildermgr-package",
		packageBuildConcurrency(), buildTriggerPredicate()); err != nil {
		return fmt.Errorf("unable to register package reconciler: %w", err)
	}

	// Cross-process propagation: under dynamic tenancy keep buildermgr's resolver
	// in step with the FissionTenant set so a runtime-onboarded namespace's
	// Packages reach the membership predicate (and build) without a restart. The
	// cluster-wide cache + RBAC are already in place in this mode; AddResolverSync
	// is a no-op when dynamic tenancy is off.
	if err := tenant.AddResolverSync(mgr); err != nil {
		return fmt.Errorf("unable to add tenant resolver-sync: %w", err)
	}

	readiness := &readinessRunnable{logger: bmLogger, cache: mgr.GetCache()}
	if err := mgr.Add(readiness); err != nil {
		return fmt.Errorf("unable to add readiness runnable: %w", err)
	}
	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return fmt.Errorf("unable to add healthz check: %w", err)
	}
	if err := mgr.AddReadyzCheck("caches-synced", readiness.check); err != nil {
		return fmt.Errorf("unable to add readyz check: %w", err)
	}

	bmLogger.Info("starting builder manager", "leaderElection", leaderElectionEnabled)
	return mgr.Start(ctx)
}

// packageBuildConcurrency resolves the package reconciler's
// MaxConcurrentReconciles from BUILDERMGR_PACKAGE_CONCURRENCY, falling back to
// defaultPackageBuildConcurrency for an unset/invalid/non-positive value.
func packageBuildConcurrency() int {
	if v, err := strconv.Atoi(os.Getenv("BUILDERMGR_PACKAGE_CONCURRENCY")); err == nil && v > 0 {
		return v
	}
	return defaultPackageBuildConcurrency
}

// readinessRunnable backs /readyz: it reports ready once this replica is the
// elected leader AND the Manager's informer caches have synced — the same
// semantics the previous informer-hosting runnable enforced (a non-leader
// replica reports not-ready). It hosts no controllers; the Environment and
// Package reconcilers are registered directly on the Manager.
type readinessRunnable struct {
	logger logr.Logger
	cache  cache.Cache
	ready  atomic.Bool
}

// NeedLeaderElection makes the Manager run this runnable on the leader only, so
// /readyz reflects leadership.
func (r *readinessRunnable) NeedLeaderElection() bool { return true }

// Start flips ready once the caches sync, then blocks until ctx is cancelled
// (shutdown or loss of leadership).
func (r *readinessRunnable) Start(ctx context.Context) error {
	if r.cache.WaitForCacheSync(ctx) {
		r.ready.Store(true)
		r.logger.Info("builder manager informer caches synced; ready")
	}
	<-ctx.Done()
	r.ready.Store(false)
	return nil
}

// check backs /readyz: ready once this replica is the leader and its caches have
// synced.
func (r *readinessRunnable) check(_ *http.Request) error {
	if r.ready.Load() {
		return nil
	}
	return fmt.Errorf("builder manager informers not synced")
}
