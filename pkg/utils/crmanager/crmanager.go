// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package crmanager builds controller-runtime Managers for control-plane
// subsystems that need only native leader election (no Manager-owned metrics or
// health servers). The trigger subsystems (kubewatcher, timer, mqtrigger,
// mqt_keda, canaryconfigmgr) use it to run their existing controllers as
// leader-only runnables. Components that also want the Manager's metrics/health
// servers (router, executor, buildermgr) build their Manager inline instead.
package crmanager

import (
	"context"
	"os"
	"strconv"

	"github.com/go-logr/logr"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/utils"
)

// NewLeaderElected builds a Manager whose only job is native leader election.
// Its metrics and health-probe servers are disabled (the subsystem serves its
// own, if any). Leader election is opt-in via LEADER_ELECTION_ENABLED; when
// disabled the Manager runs every runnable unconditionally, preserving
// single-replica behaviour. The lease namespace is auto-detected in-cluster.
// lockName is the Lease name (e.g. "fission-timer") and must be unique per
// subsystem. On losing the lease the Manager stops, so the pod exits and
// rejoins as a standby.
func NewLeaderElected(restConfig *rest.Config, lockName string, logger logr.Logger) (ctrl.Manager, error) {
	enabled, _ := strconv.ParseBool(os.Getenv("LEADER_ELECTION_ENABLED"))
	return ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                        scheme.Scheme,
		Metrics:                       metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress:        "0",
		Cache:                         FissionCacheOptions(),
		LeaderElection:                enabled,
		LeaderElectionID:              lockName,
		LeaderElectionReleaseOnCancel: true,
		Logger:                        logger,
	})
}

// FissionCacheOptions scopes a Manager's shared cache to exactly the namespaces
// Fission watches (DefaultNSResolver().FissionResourceNS — driven by
// FISSION_DEFAULT_NAMESPACE / FISSION_RESOURCE_NAMESPACES, defaulting to
// "default"). Reconcilers registered on the Manager watch through this cache,
// so scoping it here reproduces the per-namespace informers the subsystems used
// before the controller-runtime migration and keeps RBAC requirements
// unchanged. The cache is inert until a controller registers an informer, so
// this is harmless for Managers that only run non-reconciler runnables.
func FissionCacheOptions() cache.Options {
	if utils.DynamicNamespacesEnabled() {
		// Cluster-wide cache: Fission-CRD informers see every namespace's CRs so
		// a namespace can be onboarded/offboarded at runtime without rebuilding
		// the Manager. Reconcilers drop non-tenant objects via
		// controller.MembershipPredicate. This is the one cluster-wide read the
		// dynamic-watch design adds, and only over Fission's own CRD types —
		// these CRD-only Managers cache no core/workload type.
		return cache.Options{}
	}
	nsConfig := map[string]cache.Config{}
	for _, ns := range utils.DefaultNSResolver().FissionResourceNamespaces() {
		nsConfig[ns] = cache.Config{}
	}
	return cache.Options{DefaultNamespaces: nsConfig}
}

// LeaderRunnable adapts a leader-only function to a controller-runtime
// Runnable. The Manager invokes it only on the elected leader (or
// unconditionally when leader election is disabled).
type LeaderRunnable func(context.Context) error

func (f LeaderRunnable) Start(ctx context.Context) error { return f(ctx) }

// NeedLeaderElection marks the runnable as leader-only.
func (f LeaderRunnable) NeedLeaderElection() bool { return true }

// NonLeaderRunnable adapts a function to a controller-runtime Runnable that the
// Manager runs on every replica regardless of leadership. Use it for work that
// should also run on standbys, e.g. warming informer caches so failover is
// fast. The Manager starts non-leader runnables before leader-only ones, so a
// cache warmed here is ready by the time a LeaderRunnable consumes it.
type NonLeaderRunnable func(context.Context) error

func (f NonLeaderRunnable) Start(ctx context.Context) error { return f(ctx) }

// NeedLeaderElection marks the runnable as running on every replica.
func (f NonLeaderRunnable) NeedLeaderElection() bool { return false }
