// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"os"
	"time"

	"github.com/go-logr/logr"
	apiv1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/metrics"
	"github.com/fission/fission/pkg/utils"
)

// Per-image idle pool reaper (RFC-0012 phase 1).
//
// With the OCI producer on, every built package becomes its own per-image
// pool: N packages × poolsize warm pods. The pod idle reaper only deletes
// idle SPECIALIZED pods; an empty per-image pool deployment (its warm pods
// re-created by the deployment controller) lives forever. This reaper closes
// the economics: a per-image pool whose last activity is older than the reap
// window AND which currently serves no specialized pods is destroyed — the
// next cold start recreates it on demand (the normal pool-creation path),
// paying a kubelet-cached image pull on warm nodes.
//
// Generic env pools (imageHash == "") are NEVER reaped — exact parity with
// the pre-OCI behavior.

const (
	// defaultOCIPoolIdleReapTime is the idle window after which an empty
	// per-image pool is destroyed. Distinct from (and much longer than) the
	// specialized-pod idle reap time: recreating a pool costs a deployment
	// rollout, not just a specialize.
	defaultOCIPoolIdleReapTime = 5 * time.Minute

	// ociPoolReapInterval is how often the reap pass runs. The pass is a
	// map walk + a cache-backed pod list per idle candidate; with a
	// minutes-scale window a minute-scale pass loses nothing.
	ociPoolReapInterval = time.Minute
)

// ociPoolIdleReapTimeFromEnv reads OCI_POOL_IDLE_REAP_TIME, soft-failing to
// the default: a tuning knob must not be able to brick executor startup.
func ociPoolIdleReapTimeFromEnv(logger logr.Logger) time.Duration {
	raw := os.Getenv("OCI_POOL_IDLE_REAP_TIME")
	if raw == "" {
		return defaultOCIPoolIdleReapTime
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		logger.Error(err, "failed to parse 'OCI_POOL_IDLE_REAP_TIME' - using the default",
			"value", raw, "default", defaultOCIPoolIdleReapTime)
		return defaultOCIPoolIdleReapTime
	}
	return d
}

// reapIdlePoolsLoop periodically asks the pool actor to run a reap pass.
// Sent as an actor request so every gpm.pools access stays serialized on the
// service() goroutine — the same single-writer discipline as pool creation
// and env cleanup.
func (gpm *GenericPoolManager) reapIdlePoolsLoop(ctx context.Context) {
	ticker := time.NewTicker(ociPoolReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		gpm.requestChannel <- &request{
			ctx:         ctx,
			requestType: REAP_IDLE_POOLS,
		}
	}
}

// handleReapIdlePools runs one reap pass. Runs on the actor goroutine.
func (gpm *GenericPoolManager) handleReapIdlePools(req *request) {
	for key, pool := range gpm.pools {
		if pool == nil || pool.ociImageHash == "" {
			// Generic env pools are never reaped.
			continue
		}
		// The effective idle window is never shorter than the pool's
		// pod-ready timeout plus slack: a cold start can legitimately block
		// in choosePod for the full pod-ready window (image pull on a cold
		// node — the OCI case exactly), and lastActive is touched at
		// GET_POOL time. With window == timeout the reaper could destroy a
		// pool at the finish line of its first cold start. choosePod also
		// re-touches the clock when it claims a pod (belt and braces).
		window := gpm.ociPoolIdleReapTime
		if minWindow := pool.podReadyTimeout + time.Minute; window < minWindow {
			window = minWindow
		}
		if time.Unix(0, pool.lastActive.Load()).After(time.Now().Add(-window)) {
			continue
		}
		busy, err := gpm.poolHasSpecializedPods(req.ctx, pool)
		if err != nil {
			// Fail safe: never reap on uncertainty; the next pass retries.
			gpm.logger.Error(err, "skipping idle pool reap; could not list its pods",
				"poolKey", key)
			continue
		}
		if busy {
			continue
		}
		gpm.logger.Info("reaping idle per-image pool",
			"poolKey", key,
			"environment", pool.env.Name,
			"namespace", pool.env.Namespace,
			"idle_since", time.Unix(0, pool.lastActive.Load()).Format(time.RFC3339))
		// The map entry drops regardless of destroy's outcome so the next
		// GET_POOL (serialized behind this handler) recreates cleanly —
		// on-demand pool creation IS the cold-start path, and destroy has
		// already shut the ready-pod queue down. The destroy itself is
		// bounded so an API-server outage cannot stall the pool actor (and
		// with it every cold start) for longer than one pass.
		delete(gpm.pools, key)
		gpm.readyPodQueues.Delete(key)
		destroyCtx, cancel := context.WithTimeout(req.ctx, 30*time.Second)
		err = pool.destroy(destroyCtx)
		cancel()
		if err != nil {
			// The deployment leaks until a recreate adopts it by name or
			// CleanupOldExecutorObjects sweeps it on restart; count it
			// separately so the Gate C reap counter never lies.
			gpm.logger.Error(err, "error destroying reaped pool; the deployment is orphaned until adoption or restart cleanup", "poolKey", key)
			metrics.OCIPoolReapFailures.Inc()
			continue
		}
		metrics.OCIPoolsReaped.Inc()
	}
}

// poolHasSpecializedPods reports whether any pod of this per-image pool has
// been specialized (relabeled managed!=true at specialize time). Warm pods
// (managed=true) belong to the deployment and die with it; specialized pods
// are serving a function and pin the pool. Cache-backed read.
func (gpm *GenericPoolManager) poolHasSpecializedPods(ctx context.Context, pool *GenericPool) (bool, error) {
	var podList apiv1.PodList
	if err := gpm.crClient.List(ctx, &podList,
		client.InNamespace(pool.fnNamespace),
		client.MatchingLabels{fv1.POOL_OCI_IMAGE_HASH: pool.ociImageHash}); err != nil {
		return false, err
	}
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Labels["managed"] == "true" {
			continue
		}
		if utils.IsPodTerminated(pod) {
			continue
		}
		return true, nil
	}
	return false, nil
}
