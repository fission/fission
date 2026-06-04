// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"context"
	"time"

	"github.com/go-logr/logr"
)

// idleBuilderReaper is a leader-only Manager Runnable that periodically scales
// idle builder deployments to zero. It is the scale-DOWN half of the builder
// pool: the PackageReconciler scales builders UP on build demand, and this reaper
// returns them to zero once no builds have run for the environment's idle window
// (spec.builder.idleTimeout). Scaling down is never performed while a build is in
// flight, so a pod is never terminated underneath a running build.
type idleBuilderReaper struct {
	logger   logr.Logger
	poolMgr  *BuilderPoolManager
	scale    deploymentScaler
	interval time.Duration
}

func newIdleBuilderReaper(logger logr.Logger, poolMgr *BuilderPoolManager, scale deploymentScaler, interval time.Duration) *idleBuilderReaper {
	return &idleBuilderReaper{
		logger:   logger.WithName("idle_builder_reaper"),
		poolMgr:  poolMgr,
		scale:    scale,
		interval: interval,
	}
}

// NeedLeaderElection runs the reaper on the elected leader only, so exactly one
// replica scales builders down (matching readinessRunnable's leader scoping).
func (r *idleBuilderReaper) NeedLeaderElection() bool { return true }

// Start sweeps idle builders every r.interval until ctx is cancelled (shutdown or
// loss of leadership).
func (r *idleBuilderReaper) Start(ctx context.Context) error {
	r.logger.Info("starting idle builder reaper", "interval", r.interval)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.reap(ctx)
		}
	}
}

// reap scales every idle builder deployment to zero. It re-checks IsBuilding just
// before each scale to narrow the race with a build that started after
// ReapTargets snapshotted the pool state.
func (r *idleBuilderReaper) reap(ctx context.Context) {
	for _, t := range r.poolMgr.ReapTargets() {
		if r.poolMgr.IsBuilding(t.uid) {
			continue
		}
		if err := r.scale(ctx, t.builderNS, t.builderName, 0); err != nil {
			r.logger.Error(err, "failed to scale idle builder to zero",
				"builder", t.builderName, "namespace", t.builderNS, "env", t.envName)
			continue
		}
		r.poolMgr.MarkScaledToZero(t.uid)
		r.logger.Info("scaled idle builder to zero",
			"builder", t.builderName, "namespace", t.builderNS, "env", t.envName)
	}
}
