# RFC-0006: Runtime Error-Noise Reduction & Pod-Lifecycle Correctness

- Status: Implemented (PRs #3468, #3469, #3470, #3471 — 2026-06-04; follow-ups #3472, #3473 — 2026-06-05)
- Tracking issue: TBD (local-only doc; PRs are self-contained)
- Supersedes: —
- Targets: Fission v1.25.x
- Requires: Kubernetes 1.30+ for the native `sleep` lifecycle handler (our floor is 1.32 — comfortable).
- Source: error analysis of the kind v1.36.1 CI run logs (`kind-logs-26955102875-v1.36.1`, 2026-06-04).

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the five classes of recurring runtime errors found in the CI log analysis, in **three PRs**.

**Architecture:** Three independent, individually-revertable PRs: (1) replace the broken `exec /bin/sleep` preStop hooks with the native k8s `sleep` lifecycle handler; (2) make deletion paths NotFound-tolerant and fix misleading error logs; (3) stop dropping kubewatcher events on transient router 404s and give CI a working metrics pipeline for HPA.

**Tech Stack:** Go 1.26, k8s.io/api v0.36 (`core/v1.SleepAction`), controller-runtime, testify, httptest.

---

## Background — findings from the log analysis

| # | Finding | Evidence (one CI run) |
|---|---|---|
| 1 | `exec /bin/sleep <grace>` preStop hook fails on **every** pod termination: fetcher image (`chainguard/static`) has no `/bin/sleep` (273×); runtime containers sleep the full grace period so kubelet SIGKILLs the hook → exit 137 (23×); grace=0 produces a pointless `sleep 0` exec round-trip (192×) | 299 kubelet `PreStop hook failed` + 276 `ExecSync failed` + all containerd errors |
| 2 | Deletion races logged as errors: pool destroy on already-deleted Deployment; `fsvc not found in cache` on fn delete; `setInitialBuildStatus` on a deleted Package; router tap of an expired fsvc | executor 3×, buildermgr 3×, router 8× per run |
| 3 | kubewatcher events **dropped** when the router returns 404 during trigger→route propagation (publisher treats all 4xx as terminal, no retry) | 8 dropped events in one test |
| 4 | buildermgr logs builder-clean failures as raw JSON (`Internal error - {"artifactFilename":...,"buildLogs":""}`) with no cause; builder-side Clean handler loses the underlying error | 2× per run |
| 5 | CI kind cluster has no metrics-server → every newdeploy HPA logs `unable to fetch metrics ... (get pods.metrics.k8s.io)` forever; HPA scaling is never actually exercised by tests | 12 KCM errors per run |

Non-Fission noise (no action): kubelet `Failed to get status for pod ... forbidden` (NodeRestriction churn race, 1344×), KCM replicaset `read version not as new as written` (k8s 1.36 watch-cache retries, 164×), prometheus-operator webhook failing open (58×), invalidated SA tokens (5×).

---

# PR 1 — `fix/prestop-native-sleep`: native sleep preStop hook

**Branch:** `fix/prestop-native-sleep` off `main`.

**Why:** The hook's purpose (k8s issue 47576: keep the pod alive while endpoints/router drop it from rotation) is valid, but `exec /bin/sleep` (a) cannot work on distroless images, (b) always exits 137 because it sleeps the entire grace window, (c) is a wasted CRI round-trip at grace=0. The native `sleep` handler (GA k8s 1.30) is executed by the kubelet itself — no binary needed, no exec, and it is terminated cleanly at grace expiry.

**Behavior decisions:**
- Keep the sleep duration == grace period (current semantics, just native). Changing to a bounded drain window is a separate product discussion — out of scope.
- When grace == 0, set **no** lifecycle hook at all (k8s validation rejects `sleep: {seconds: 0}` without the `PodLifecycleSleepActionAllowZero` feature gate, and it's semantically a no-op anyway).

### Task 1.1: shared helper

**Files:**
- Create: `pkg/utils/lifecycle.go`
- Test: `pkg/utils/lifecycle_test.go`

`pkg/utils` is dependency-free (no fetcher/executor imports), so both `pkg/executor/*` and `pkg/fetcher/config` can use it without an import cycle.

- [ ] **Step 1: Write the failing test** (`pkg/utils/lifecycle_test.go`):

```go
package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDrainLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("positive grace returns native sleep preStop", func(t *testing.T) {
		t.Parallel()
		lc := DrainLifecycle(360)
		require.NotNil(t, lc)
		require.NotNil(t, lc.PreStop)
		require.NotNil(t, lc.PreStop.Sleep)
		assert.Nil(t, lc.PreStop.Exec)
		assert.EqualValues(t, 360, lc.PreStop.Sleep.Seconds)
	})

	t.Run("zero grace returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, DrainLifecycle(0))
	})

	t.Run("negative grace returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, DrainLifecycle(-1))
	})
}
```

- [ ] **Step 2: Run it, verify it fails** — `go test -run TestDrainLifecycle ./pkg/utils/` → FAIL: `undefined: DrainLifecycle`.

- [ ] **Step 3: Implement** (`pkg/utils/lifecycle.go`, run `make license` after creating):

```go
package utils

import (
	apiv1 "k8s.io/api/core/v1"
)

// DrainLifecycle returns a preStop lifecycle that keeps a terminating pod
// alive for the full grace period so the router/endpoints controller can
// drop it from rotation before the process is killed (connection draining,
// see https://github.com/kubernetes/kubernetes/issues/47576#issuecomment-308900172).
//
// It uses the kubelet-native sleep action (GA since Kubernetes 1.30) instead
// of `exec /bin/sleep`: distroless images (e.g. the fetcher) have no sleep
// binary, and an exec'd sleep spanning the whole grace window is always
// SIGKILLed at expiry, failing the hook on every termination.
//
// A non-positive grace returns nil: there is no drain window to hold open,
// and Kubernetes validation rejects sleep actions of zero seconds.
func DrainLifecycle(gracePeriodSeconds int64) *apiv1.Lifecycle {
	if gracePeriodSeconds <= 0 {
		return nil
	}
	return &apiv1.Lifecycle{
		PreStop: &apiv1.LifecycleHandler{
			Sleep: &apiv1.SleepAction{Seconds: gracePeriodSeconds},
		},
	}
}
```

- [ ] **Step 4: Run tests** — `go test -run TestDrainLifecycle ./pkg/utils/` → PASS.
- [ ] **Step 5: Commit** — `git add pkg/utils/lifecycle.go pkg/utils/lifecycle_test.go && git commit -m "feat(utils): DrainLifecycle helper using native sleep preStop"`

### Task 1.2: poolmgr

**Files:**
- Modify: `pkg/executor/executortype/poolmgr/gp_deployment.go:105-120`
- Test: `pkg/executor/executortype/poolmgr/gp_deployment_test.go` (extend existing)

- [ ] **Step 1:** In `genDeploymentSpec`, replace the `Lifecycle:` block of the runtime container (keep the explanatory comment, drop the exec):

```go
			// Pod is removed from endpoints list for service when it's
			// state became "Termination". We used preStop hook as the
			// workaround for connection draining since pod maybe shutdown
			// before grace period expires.
			// https://kubernetes.io/docs/concepts/workloads/pods/pod/#termination-of-pods
			// https://github.com/kubernetes/kubernetes/issues/47576#issuecomment-308900172
			Lifecycle: utils.DrainLifecycle(gracePeriodSeconds),
```

Import `"github.com/fission/fission/pkg/utils"` (check it isn't already imported under another name; goimports local prefix is `github.com/fission/fission`).

- [ ] **Step 2:** Extend `gp_deployment_test.go` with a subtest asserting: for an env with `TerminationGracePeriod: 0` the runtime container has nil `Lifecycle`; for the default (360s) it has `PreStop.Sleep.Seconds == 360` and nil `PreStop.Exec`. Follow the existing test style in that file (table-driven, fake env objects).
- [ ] **Step 3:** `go test -race ./pkg/executor/executortype/poolmgr/` → PASS.
- [ ] **Step 4: Commit** — `git commit -m "fix(poolmgr): native sleep preStop, skip hook at grace=0"`

### Task 1.3: newdeploy + container executors

**Files:**
- Modify: `pkg/executor/executortype/newdeploy/newdeploy.go:175-184`
- Modify: `pkg/executor/executortype/container/deployment.go:179-188`

- [ ] **Step 1:** Same replacement in both files:

```go
		Lifecycle: utils.DrainLifecycle(gracePeriodSeconds),
```

(both functions already have `gracePeriodSeconds int64` in scope; add the `pkg/utils` import).

- [ ] **Step 2:** `go build ./pkg/... && go test -race ./pkg/executor/...` → PASS.
- [ ] **Step 3: Commit** — `git commit -m "fix(newdeploy,container): native sleep preStop, skip hook at grace=0"`

### Task 1.4: fetcher sidecar

**Files:**
- Modify: `pkg/fetcher/config/config.go:312-329`
- Test: `pkg/fetcher/config/config_test.go` (extend if present, else add)

This is the highest-impact site: the fetcher image is `cgr.dev/chainguard/static` with no `/bin/sleep`, so today the hook fails on 100% of poolmgr/newdeploy pod terminations.

- [ ] **Step 1:** Replace the block:

```go
	// Pod is removed from endpoints list for service when it's
	// state became "Termination". We used preStop hook as the
	// workaround for connection draining since pod maybe shutdown
	// before grace period expires.
	// https://kubernetes.io/docs/concepts/workloads/pods/pod/#termination-of-pods
	// https://github.com/kubernetes/kubernetes/issues/47576#issuecomment-308900172
	// NOTE: must be the kubelet-native sleep action — the fetcher image is
	// distroless (chainguard/static) and has no /bin/sleep to exec.
	if podSpec.TerminationGracePeriodSeconds != nil {
		c.Lifecycle = utils.DrainLifecycle(*podSpec.TerminationGracePeriodSeconds)
	}
```

- [ ] **Step 2:** Add/extend a unit test on the function that injects the fetcher container (`AddFetcherToPodSpec` path): grace=360 → `Sleep` hook; grace=0 → nil `Lifecycle`.
- [ ] **Step 3:** `go test -race ./pkg/fetcher/...` → PASS.
- [ ] **Step 4: Commit** — `git commit -m "fix(fetcher): native sleep preStop (image has no /bin/sleep)"`

### Task 1.5: verify + PR

- [ ] `make code-checks` → 0 issues; `go build ./pkg/... ./cmd/...` → clean.
- [ ] `grep -rn '"/bin/sleep"' pkg/` → no hits remain.
- [ ] Push branch; PR title: `fix(executor,fetcher): use native sleep preStop hook instead of exec /bin/sleep`. PR body: cite the three failure flavors with kubelet counts (299 hook failures / 276 ExecSync / exit-137) and the k8s 1.30 GA reference; note k8s floor 1.32.
- [ ] Post-PR: drive CI green. The integration suite exercises pod creation/termination on all three executor types — a wrong hook shape fails fast at Deployment validation.
- [ ] **Manual verification (post-merge or on a kind cluster):** `kubectl get events -A --field-selector reason=FailedPreStopHook` during a test run → empty.

---

# PR 2 — `fix/deletion-race-noise`: NotFound-tolerant deletion + honest error logs

**Branch:** `fix/deletion-race-noise` off `main`.

**Why:** Every error here fires during normal deletion churn and trains operators to ignore the error log. Deletion of an already-deleted thing is success, not an error.

### Task 2.1: poolmgr pool destroy

**Files:**
- Modify: `pkg/executor/executortype/poolmgr/gp.go:698-705`
- Modify: `pkg/executor/executortype/poolmgr/gpm.go:535`

- [ ] **Step 1:** In `gp.go` (`destroyDeployment` / the deployment delete at line 698), tolerate NotFound:

```go
	err := gp.kubernetesClient.AppsV1().
		Deployments(gp.fnNamespace).Delete(ctx, gp.deployment.Name, delOpt)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			// Already gone (e.g. namespace teardown raced us) — destroy is
			// idempotent, nothing left to do.
			gp.logger.V(1).Info("deployment already deleted",
				"deployment_name", gp.deployment.Name,
				"deployment_namespace", gp.fnNamespace)
			return nil
		}
		gp.logger.Error(err, "error destroying deployment", "deployment_name", gp.deployment.Name,
			"deployment_namespace", gp.fnNamespace)
		return err
	}
	return nil
```

Use whatever alias the file already has for `k8s.io/apimachinery/pkg/api/errors` (add it if missing).

- [ ] **Step 2:** In `gpm.go:535`, `"Could not find pool"` on CLEANUP_POOL is the same already-cleaned case — change `gpm.logger.Error(nil, ...)` to `gpm.logger.Info("pool already removed", ...)`.
- [ ] **Step 3:** `go test -race ./pkg/executor/executortype/poolmgr/` → PASS.
- [ ] **Step 4: Commit** — `git commit -m "fix(poolmgr): treat NotFound as success when destroying pools"`

### Task 2.2: fn delete with missing fsvc cache entry (newdeploy + container)

**Files:**
- Modify: `pkg/executor/executortype/newdeploy/newdeploymgr.go:757-785` (`fnDelete`)
- Modify: `pkg/executor/executortype/container/containermgr.go:~600-615` (its `fnDelete`, same shape)

A cache miss must not abort cleanup: the k8s object name is deterministic (`getObjName(fn)` derives from fn.UID), so fall back to it and still delete the Deployment/Service/HPA. This is *more* correct than today (today a cache miss leaks the k8s objects **and** logs an error).

- [ ] **Step 1:** Rework `fnDelete` in `newdeploymgr.go`:

```go
func (deploy *NewDeploy) fnDelete(ctx context.Context, fn *fv1.Function) error {
	var errs error

	// GetByFunction uses resource version as part of cache key, however,
	// the resource version in function metadata will be changed when a function
	// is deleted and cause newdeploy backend fails to delete the entry.
	// Use GetByFunctionUID instead of GetByFunction here to find correct
	// fsvc entry.
	objName := deploy.getObjName(fn)
	fsvc, err := deploy.fsCache.GetByFunctionUID(fn.UID)
	if err != nil {
		// Not in cache (e.g. never specialized, or already evicted). The
		// backing object names are deterministic, so proceed with cleanup
		// using the computed name instead of failing.
		deploy.logger.V(1).Info("fsvc not in cache, cleaning up by computed name",
			"function", k8sCache.MetaObjectToName(fn), "obj_name", objName)
	} else {
		objName = fsvc.Name
		if _, err := deploy.fsCache.DeleteOld(fsvc, time.Second*0); err != nil {
			errs = errors.Join(errs, fmt.Errorf("error deleting the function from cache"))
		}
	}

	// to support backward compatibility, if the function was created in default ns, we fall back to creating the
	// deployment of the function in fission-function ns, so cleaning up resources there
	ns := deploy.nsResolver.GetFunctionNS(fn.Namespace)

	errs = errors.Join(errs, deploy.cleanupNewdeploy(ctx, ns, objName))

	return errs
}
```

- [ ] **Step 2:** Mirror the same change in `containermgr.go`'s `fnDelete` (line ~609, message `fsvc not found in cache`); it has the same structure with its own `cleanupContainer`-style call — keep that call name as-is.
- [ ] **Step 3:** Verify `cleanupNewdeploy` (and the container equivalent) already tolerate NotFound on the individual deletes — read them; if they propagate NotFound, wrap those deletes with the same `IsNotFound → nil` guard.
- [ ] **Step 4:** `go test -race ./pkg/executor/...` → PASS.
- [ ] **Step 5: Commit** — `git commit -m "fix(executor): fn delete cleans up by computed name on fsvc cache miss"`

### Task 2.3: buildermgr Package reconciler delete race

**Files:**
- Modify: `pkg/buildermgr/package_reconciler.go:85-87`

- [ ] **Step 1:**

```go
		if _, err := setInitialBuildStatus(ctx, r.fissionClient, pkg); err != nil {
			if apierrors.IsNotFound(err) {
				// Package deleted between our Get and the status write —
				// nothing to initialize.
				return ctrl.Result{}, nil
			}
			return ctrl.Result{}, fmt.Errorf("error setting initial package build status: %w", err)
		}
```

(`apierrors` is already imported in this file for the Get path.)

- [ ] **Step 2:** `go test -race ./pkg/buildermgr/` → PASS.
- [ ] **Step 3: Commit** — `git commit -m "fix(buildermgr): ignore NotFound when initializing build status of a deleted package"`

### Task 2.4: buildermgr clean-failure log quality (+ builder reply investigation)

**Files:**
- Modify: `pkg/buildermgr/common.go:67-74`
- Read first: `pkg/builder/builder.go:190-230` (`Clean` handler) and `pkg/builder/client/client.go` (`Clean`)

The observed log is `error cleaning src pkg from builder storage: Internal error - {"artifactFilename":"...","buildLogs":""}` — the JSON is the builder's reply struct serialized into the error string with the actual cause missing.

- [ ] **Step 1 (investigate, 5 min):** Read `pkg/builder/builder.go` `Clean` + `reply` and the client's response handling to find where the cause is dropped (empty `buildLogs` on the 500 path). Note: the builder binary ships in **pre-built env-builder images on GHCR** — a fix in `pkg/builder` will NOT show up in CI integration tests until those images are rebuilt. Fix it anyway (it lands in the next env-image release); the buildermgr-side fix below is per-PR and takes effect immediately.
- [ ] **Step 2:** In `pkg/builder/builder.go` `Clean`'s failure path, put the error into the reply: `builder.reply(r.Context(), w, srcPkgFilename, fmt.Sprintf("error cleaning package: %v", err), http.StatusInternalServerError)` (match the existing reply call shape at line 210).
- [ ] **Step 3:** In `pkg/buildermgr/common.go` deferred cleanup, add context and tolerate already-gone packages:

```go
	defer func() {
		logger.Info("cleaning src pkg from builder storage", "source_package", srcPkgFilename)
		if errC := cleanPackage(ctx, builderC, srcPkgFilename); errC != nil {
			if ferror.IsNotFound(errC) {
				return // already gone — fine
			}
			logger.Error(errC, "error cleaning src pkg from builder storage",
				"source_package", srcPkgFilename,
				"package", pkg.Name, "package_namespace", pkg.Namespace)
		}
	}()
```

(`pkg/error`'s `IsNotFound` is at `pkg/error/httperror.go:109`; check the import alias used in this file — it's `ferror` elsewhere in the package.)

- [ ] **Step 4:** `go build ./pkg/... && go test -race ./pkg/buildermgr/ ./pkg/builder/...` → PASS.
- [ ] **Step 5: Commit** — `git commit -m "fix(buildermgr,builder): carry the real cause in clean-package errors"`

### Task 2.5: tap-service churn logged at error level

**Files:**
- Modify: `pkg/executor/api.go:186-209` (`tapServices`)
- Modify: `pkg/executor/client/client.go:170-173` (router-side batch tap)

Tap is a best-effort atime refresh; an expired/deleted fsvc is routine churn. Today executor logs `error tapping function service` and router logs `error tapping function service address` for the same routine event (8×/run).

- [ ] **Step 1:** In `api.go` `tapServices`, split NotFound from real errors. `et.TapService` returns the fscache error; collect two error sets:

```go
	var errs, notFound error
	for _, req := range tapSvcReqs {
		svcHost := strings.TrimPrefix(req.ServiceURL, "http://")

		et, exists := executor.executorTypes[req.FnExecutorType]
		if !exists {
			errs = errors.Join(errs,
				fmt.Errorf("error tapping service due to unknown executor type '%s' found",
					req.FnExecutorType))
			continue
		}

		if err := et.TapService(ctx, svcHost); err != nil {
			wrapped := fmt.Errorf("error tapping function '%s/%s' with executor '%s' and service url '%s': %w", req.FnMetadata.Namespace, req.FnMetadata.Name, req.FnExecutorType, req.ServiceURL, err)
			if ferror.IsNotFound(err) {
				notFound = errors.Join(notFound, wrapped)
			} else {
				errs = errors.Join(errs, wrapped)
			}
		}
	}

	if errs != nil {
		logger.Error(errs, "error tapping function service")
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if notFound != nil {
		// Expired/deleted fsvcs are routine churn — the router's entry is
		// stale, not broken. Still a 404 so the caller knows, but not an error log.
		logger.V(1).Info("tap skipped for expired function services", "detail", notFound.Error())
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
```

Check what error type fscache's miss actually returns (`pkg/executor/fscache`) — if it isn't a `ferror` NotFound, match on that type instead.

- [ ] **Step 2:** In `client.go:172`, the batch tap failure is best-effort — downgrade: `c.logger.V(1).Info("error tapping function service address", "error", err.Error())`.
- [ ] **Step 3:** `go test -race ./pkg/executor/...` → PASS; `make code-checks` → 0 issues.
- [ ] **Step 4: Commit** — `git commit -m "fix(executor): stop logging routine tap-service churn as errors"`

### Task 2.6: verify + PR

- [ ] `make code-checks` + `go build ./pkg/... ./cmd/...` + `go test -race ./pkg/executor/... ./pkg/buildermgr/... ./pkg/builder/... ./pkg/utils/...` → all green.
- [ ] Push; PR title: `fix: tolerate deletion races and stop logging routine churn as errors`. Body: table of the five sites with before/after log behavior, citing per-run counts from the analysis.
- [ ] Post-PR: CI green loop.

---

# PR 3 — `fix/kubewatcher-404-retry`: don't drop events on transient router 404 + CI metrics-server

**Branch:** `fix/kubewatcher-404-retry` off `main`.

**Why (404 retry):** `webhookPublisher.makeHTTPRequest` treats every 4xx as terminal. But the router returns 404 for `/fission-function/...` during the window between trigger creation and mux reconciliation — kubewatcher/timer/mqtrigger events fired in that window are silently dropped (8 observed in one run). A 404 is transient in this architecture; retrying it (bounded by the existing `maxRetries: 10`, exponential backoff from 500ms ≈ 4½ min worst case) converts dropped events into delivered ones once the route lands. For a genuinely deleted function, we burn 10 cheap retries and give up — acceptable.

**Why (metrics-server):** newdeploy HPAs target CPU utilization via `pods.metrics.k8s.io`, which doesn't exist in the CI kind cluster — HPA reconciliation fails forever and tests never exercise scaling.

### Task 3.1: retry 404 in webhookPublisher

**Files:**
- Modify: `pkg/publisher/webhookPublisher.go:186-196`
- Test: `pkg/publisher/publisher_test.go` (extend)

- [ ] **Step 1: Write the failing test** in `publisher_test.go`, following the file's existing httptest patterns:

```go
func TestWebhookPublisherRetriesNotFound(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		if hits < 3 {
			http.NotFound(w, r) // route not reconciled yet
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := MakeWebhookPublisher(logr.Discard(), srv.URL)
	p.Publish(t.Context(), "body", map[string]string{}, "GET", "fn")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return hits >= 3
	}, 10*time.Second, 50*time.Millisecond, "publisher should retry past transient 404s")
}
```

Adapt the constructor/Publish signatures to what the file actually exports (check `MakeWebhookPublisher`'s parameters at `webhookPublisher.go:83` area). A 2×-backoff from 500ms reaches the 3rd attempt in ~1.5s — inside the Eventually window.

- [ ] **Step 2: Run it, verify it fails** — `go test -run TestWebhookPublisherRetriesNotFound ./pkg/publisher/` → FAIL (hits stays 1).

- [ ] **Step 3: Implement** — in `makeHTTPRequest`, make 404 fall through to the retry block instead of returning:

```go
			logger = logger.WithValues("status_code", resp.StatusCode, "body", string(body))
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				msgType = "info"
				return
			} else if resp.StatusCode == http.StatusNotFound {
				// The router returns 404 while a freshly created trigger's
				// route is still propagating to the mux; treat it as
				// transient and retry (bounded by maxRetries) instead of
				// dropping the event.
				msg = "request returned not found, will retry"
			} else if resp.StatusCode < 500 {
				msg = "request returned bad request status code"
				return
			} else {
				msg = "request returned failure status code"
				return
			}
```

(Note the restructure: success and non-404 4xx/5xx terminal paths `return` explicitly; 404 falls through to the existing retry scheduling below. Today 5xx also returns without retrying — *verify* that reading the current control flow: the `return` at line 195 covers all parsed-status cases, so 5xx currently never retries either; only transport errors do. Decide deliberately: keep 5xx terminal (status quo) and retry only 404, which is the minimal change for the observed loss.)

- [ ] **Step 4:** `go test -race ./pkg/publisher/` → PASS (new + existing tests).
- [ ] **Step 5: Commit** — `git commit -m "fix(publisher): retry webhook delivery on transient router 404"`

### Task 3.2: metrics-server in CI

**Files:**
- Modify: `.github/workflows/push_pr.yaml:103-110` (the helm-install step that installs kube-prometheus-stack)

- [ ] **Step 1:** Add to the same step that installs prometheus:

```yaml
          helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/
          helm install metrics-server metrics-server/metrics-server \
            -n kube-system --set 'args={--kubelet-insecure-tls}' --wait --timeout 2m
```

(`--kubelet-insecure-tls` is required on kind — kubelet serving certs aren't signed for the node IP.)

- [ ] **Step 2:** Validate YAML: `ruby -ryaml -e 'YAML.load_file(".github/workflows/push_pr.yaml"); puts "OK"`.
- [ ] **Step 3: Commit** — `git commit -m "ci: install metrics-server so newdeploy HPAs can compute"`

### Task 3.3: verify + PR

- [ ] `make code-checks` → 0 issues.
- [ ] Push; PR title: `fix(publisher): retry transient 404s; ci: metrics-server for HPA`. Body: explain the dropped-event window with the log evidence, and the KCM `pods.metrics.k8s.io` errors.
- [ ] Post-PR: CI green loop. On the CI run, spot-check the kubewatcher test leg and confirm KCM HPA errors are gone from any fission-dump artifacts.
- [ ] **Follow-up (out of scope, note in PR):** an integration test asserting an actual HPA scale event on a newdeploy function under load is now possible — file as an issue.

---

## Self-review checklist (done at plan-write time)

- Finding 1 → PR 1 (all four `/bin/sleep` sites). Finding 2 → PR 2 Tasks 2.1–2.3, 2.5. Finding 4 → PR 2 Task 2.4. Finding 3 → PR 3 Task 3.1. Finding 5 → PR 3 Task 3.2. Non-Fission noise → explicitly no action. ✓
- `DrainLifecycle` name used consistently across Tasks 1.1–1.4. ✓
- Known caveats encoded: builder image is pre-built GHCR (Task 2.4 Step 1), `SleepAction` exists in k8s.io/api v0.36.1, `ferror.IsNotFound` at `pkg/error/httperror.go:109`, publisher retry params (`maxRetries: 10`, 500ms × 2 backoff) at `webhookPublisher.go:83-84`. ✓
- Each PR is independently revertable and CI-verifiable. ✓
