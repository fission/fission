// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	k8sCache "k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/leaderelection"
	"github.com/fission/fission/pkg/utils/manager"
	fissionmetrics "github.com/fission/fission/pkg/utils/metrics"
)

// leaderElectionID is the name of the Lease the builder manager contends for.
// Kept identical to the client-go lease name used before the controller-runtime
// migration so there is no orphaned lease across the upgrade.
const leaderElectionID = "fission-buildermgr"

// Start runs the builder manager under a controller-runtime Manager. The
// Manager owns leader election, health/readiness probes, the metrics server and
// graceful shutdown. The environment/package watchers keep their existing
// informer-driven logic; they run as a leader-only Manager runnable. The legacy
// GroupManager argument is unused now that the Manager owns the lifecycle.
func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, _ manager.Interface, storageSvcUrl string) error {
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

	envWatcher, err := makeEnvironmentWatcher(ctx, bmLogger, fissionClient, kubernetesClient, fConfig, podSpecPatch)
	if err != nil {
		return err
	}

	pkgWatcher := makePackageWatcher(bmLogger, fissionClient,
		kubernetesClient, storageSvcUrl,
		utils.GetK8sInformersForNamespaces(kubernetesClient, time.Minute*30, fv1.Pods),
		utils.GetInformersForNamespaces(fissionClient, time.Minute*30, fv1.PackagesResource))

	leaderElectionEnabled, _ := strconv.ParseBool(os.Getenv("LEADER_ELECTION_ENABLED"))
	leNamespace := leaderelection.Namespace()
	if leaderElectionEnabled && leNamespace == "" {
		return fmt.Errorf("leader election enabled but pod namespace is unknown; set POD_NAMESPACE")
	}

	// Fission's custom collectors register into controller-runtime's global
	// metrics registry; the Manager's metrics server then serves them on
	// METRICS_ADDR (preserving the existing :8080 scrape).
	if err := ctrlmetrics.Registry.Register(fissionmetrics.Registry); err != nil {
		bmLogger.Error(err, "failed to register fission metrics collectors")
	}

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                        scheme.Scheme,
		Metrics:                       metricsserver.Options{BindAddress: bindAddr("METRICS_ADDR", "8080")},
		HealthProbeBindAddress:        bindAddr("HEALTH_PROBE_ADDR", "8081"),
		LeaderElection:                leaderElectionEnabled,
		LeaderElectionID:              leaderElectionID,
		LeaderElectionNamespace:       leNamespace,
		LeaderElectionReleaseOnCancel: true,
		Logger:                        bmLogger,
	})
	if err != nil {
		return fmt.Errorf("unable to set up builder manager: %w", err)
	}

	runnable := &watcherRunnable{logger: bmLogger, env: envWatcher, pkg: pkgWatcher}
	if err := mgr.Add(runnable); err != nil {
		return fmt.Errorf("unable to add watcher runnable: %w", err)
	}
	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		return fmt.Errorf("unable to add healthz check: %w", err)
	}
	if err := mgr.AddReadyzCheck("informers-synced", runnable.readyCheck); err != nil {
		return fmt.Errorf("unable to add readyz check: %w", err)
	}

	bmLogger.Info("starting builder manager", "leaderElection", leaderElectionEnabled)
	return mgr.Start(ctx)
}

// bindAddr resolves a server bind address from env, defaulting to def and
// prefixing ":" when only a port is given (matching ServeMetrics' convention).
func bindAddr(env, def string) string {
	addr := os.Getenv(env)
	if addr == "" {
		addr = def
	}
	if !strings.Contains(addr, ":") {
		addr = ":" + addr
	}
	return addr
}

// watcherRunnable runs the environment and package watchers as a leader-only
// controller-runtime runnable. The existing informer goroutines are hosted on
// an internal GroupManager; only the elected leader runs them. When leader
// election is disabled, the Manager runs this runnable unconditionally, so
// single-replica behaviour is unchanged.
type watcherRunnable struct {
	logger logr.Logger
	env    *environmentWatcher
	pkg    *packageWatcher
	ready  atomic.Bool
}

// NeedLeaderElection makes the Manager run this runnable on the leader only.
func (w *watcherRunnable) NeedLeaderElection() bool { return true }

// Start launches the watchers and blocks until ctx is cancelled (shutdown or
// loss of leadership), then drains the informer goroutines.
func (w *watcherRunnable) Start(ctx context.Context) error {
	gm := manager.New()
	w.env.Run(ctx, gm)
	if err := w.pkg.Run(ctx, gm); err != nil {
		return err
	}
	go func() {
		if w.waitForCacheSync(ctx) {
			w.ready.Store(true)
			w.logger.Info("builder manager informer caches synced; ready")
		}
	}()
	<-ctx.Done()
	w.ready.Store(false)
	gm.Wait()
	return nil
}

func (w *watcherRunnable) waitForCacheSync(ctx context.Context) bool {
	synced := make([]k8sCache.InformerSynced, 0)
	for _, inf := range w.env.envWatchInformer {
		synced = append(synced, inf.HasSynced)
	}
	for _, inf := range w.pkg.podInformer {
		synced = append(synced, inf.HasSynced)
	}
	for _, inf := range w.pkg.pkgInformer {
		synced = append(synced, inf.HasSynced)
	}
	return k8sCache.WaitForCacheSync(ctx.Done(), synced...)
}

// readyCheck backs /readyz: ready once the watcher informers have synced (and,
// under leader election, only after this replica has won the lease).
func (w *watcherRunnable) readyCheck(_ *http.Request) error {
	if w.ready.Load() {
		return nil
	}
	return fmt.Errorf("builder manager informers not synced")
}
