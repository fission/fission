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
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
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
		LeaderElection:                enabled,
		LeaderElectionID:              lockName,
		LeaderElectionReleaseOnCancel: true,
		Logger:                        logger,
	})
}

// LeaderRunnable adapts a leader-only function to a controller-runtime
// Runnable. The Manager invokes it only on the elected leader (or
// unconditionally when leader election is disabled).
type LeaderRunnable func(context.Context) error

func (f LeaderRunnable) Start(ctx context.Context) error { return f(ctx) }

// NeedLeaderElection marks the runnable as leader-only.
func (f LeaderRunnable) NeedLeaderElection() bool { return true }
