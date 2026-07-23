// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/client"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/versioning"
	"github.com/fission/fission/test/integration/framework"
)

// TestVersionedSpecialize is the RFC-0025 phase-2 end-to-end proof: two
// published versions of one Function specialize side by side through the
// executor's direct /v2/getServiceForFunction API (there is no router-side
// versioned route yet — that is phase 3), each getting its own per-version
// headless Service and its own generation-labeled pod, and the idle reaper's
// alias-retention (pkg/executor/versionretain) keeps an aliased, otherwise-idle
// old version warm until the alias moves away.
func TestVersionedSpecialize(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)
	acquireHeavySlot(t)

	ns := f.NewTestNamespace(t)
	fc := f.FissionClient().CoreV1()

	envName := "nodejs-verspec-" + ns.ID
	fnName := "fn-verspec-" + ns.ID
	aliasName := "keepwarm-" + ns.ID
	v1Name := fnName + "-v1"
	v2Name := fnName + "-v2"

	// idleTimeoutSeconds is set low (well below the default 120s) so the
	// warm-retention leg fits a reasonable test budget. windowMultiplier
	// bounds how long we hold traffic off v1 before asserting it survived —
	// generous relative to idleTimeoutSeconds so a slow reap tick or
	// versionretain sync doesn't flake the assertion.
	const idleTimeoutSeconds = 30
	const windowMultiplier = 4
	// The function deliberately uses the CLI's default RetainPods (0, i.e.
	// FunctionOptions.RetainPods left unset below): PoolCache.ListAvailableValue
	// floors svcRetain at one warm pod for a non-latest generation that
	// versionretain.View.Retained reports as alias-referenced (see
	// pkg/executor/fscache/poolcache.go), without requiring RetainPods > 0. That
	// floor is exactly the promise this test proves -- an aliased old version
	// keeps ONE pod warm at default config, no operator opt-in required.

	// Belt-and-suspenders cleanup, mirroring TestFunctionVersionPhase1: the
	// alias must go before the versions it may still reference, so a
	// mid-test failure doesn't leave the version-delete guard blocking
	// teardown. Registered before any of these resources exist.
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		_ = fc.FunctionAliases(ns.Name).Delete(cctx, aliasName, metav1.DeleteOptions{})
		_ = fc.FunctionVersions(ns.Name).Delete(cctx, v1Name, metav1.DeleteOptions{})
		_ = fc.FunctionVersions(ns.Name).Delete(cctx, v2Name, metav1.DeleteOptions{})
	})

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})

	codeV1 := writeNodeReturning(t, "v1", "v1!\n")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name:        fnName,
		Env:         envName,
		Code:        codeV1,
		IdleTimeout: idleTimeoutSeconds,
	})
	ns.WaitForFunction(t, ctx, fnName)

	// --- publish v1 ---
	ns.CLI(t, ctx, "fn", "publish", "--name", fnName, "--wait")
	v1, err := fc.FunctionVersions(ns.Name).Get(ctx, v1Name, metav1.GetOptions{})
	require.NoErrorf(t, err, "get FunctionVersion %q", v1Name)

	// --- runtime-affecting update, then publish v2 ---
	codeV2 := writeNodeReturning(t, "v2", "v2!\n")
	ns.CLI(t, ctx, "fn", "update", "--name", fnName, "--code", codeV2)
	ns.CLI(t, ctx, "fn", "publish", "--name", fnName, "--wait")
	v2, err := fc.FunctionVersions(ns.Name).Get(ctx, v2Name, metav1.GetOptions{})
	require.NoErrorf(t, err, "get FunctionVersion %q", v2Name)

	live := ns.GetFunction(t, ctx, fnName)

	// --- direct-specialize both versions via the executor ---
	// versioning.VersionedFunction projects live onto each snapshot the way
	// phase-3 router resolution will: same Function identity (name, UID),
	// but Spec/Generation pinned to the published version.
	specializeVersion := func(v *fv1.FunctionVersion, timeout time.Duration) (int, string, error) {
		vfn := versioning.VersionedFunction(live, v)
		sctx, scancel := context.WithTimeout(ctx, timeout)
		defer scancel()
		return postGetServiceForFunction(sctx, f, vfn)
	}
	// releaseVersion POSTs /v2/unTapService for (v, address). getServiceForFunction
	// is the executor-resolved accounting path (as opposed to the router-admitted
	// index's Release closure — see CLAUDE.md's "two disjoint modes" request-
	// accounting note): every call increments PoolCache.activeRequests for that
	// pod, and ListAvailableValue only offers an entry to the idle reaper once
	// activeRequests==0. Skipping the release would pin a pod warm forever
	// regardless of alias-retention, masking exactly the behaviour this test
	// exists to prove -- so every specialize call below is immediately released.
	releaseVersion := func(v *fv1.FunctionVersion, address string) {
		t.Helper()
		vfn := versioning.VersionedFunction(live, v)
		tctx, tcancel := context.WithTimeout(ctx, 30*time.Second)
		defer tcancel()
		status, err := postUnTapService(tctx, f, vfn.ObjectMeta, address)
		assert.NoErrorf(t, err, "POST /v2/unTapService for %s (%s)", v.Name, address)
		assert.Equalf(t, http.StatusOK, status, "unTapService for %s (%s)", v.Name, address)
	}
	mustSpecialize := func(v *fv1.FunctionVersion) string {
		t.Helper()
		status, addr, err := specializeVersion(v, 90*time.Second)
		require.NoErrorf(t, err, "POST /v2/getServiceForFunction for %s", v.Name)
		require.Equalf(t, http.StatusOK, status, "getServiceForFunction for %s: body=%s", v.Name, addr)
		require.NotEmptyf(t, addr, "getServiceForFunction for %s returned an empty address", v.Name)
		releaseVersion(v, addr)
		return addr
	}

	addr1 := mustSpecialize(v1)
	addr2 := mustSpecialize(v2)
	assert.NotEqualf(t, addr1, addr2, "v1 (%s) and v2 (%s) must specialize to distinct pods", v1Name, v2Name)

	// --- two per-version headless Services, selectors differ on generation ---
	svcSelector := labels.Set(map[string]string{
		fv1.FUNCTION_UID:     string(live.UID),
		fv1.MANAGED_BY_LABEL: fv1.MANAGED_BY_VALUE,
	}).AsSelector().String()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		svcs, err := f.KubeClient().CoreV1().Services(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: svcSelector})
		if !assert.NoError(c, err, "list per-version Services") {
			return
		}
		if !assert.Lenf(c, svcs.Items, 2, "expected 2 per-version Services for %q, got %d", fnName, len(svcs.Items)) {
			return
		}
		generations := map[string]bool{}
		for i := range svcs.Items {
			svc := &svcs.Items[i]
			generations[svc.Spec.Selector[fv1.FUNCTION_GENERATION]] = true
			assert.NotEmptyf(c, svc.Labels[fv1.FUNCTION_VERSION], "Service %s missing %s label", svc.Name, fv1.FUNCTION_VERSION)
		}
		assert.Lenf(c, generations, 2, "expected 2 distinct FUNCTION_GENERATION selectors across Services, got %v", generations)
	}, 60*time.Second, 2*time.Second)

	// --- pods for both generations exist, carrying the version label ---
	podsForGeneration := func(gen int64, requireServed bool) ([]corev1.Pod, error) {
		sel := map[string]string{
			fv1.FUNCTION_UID:        string(live.UID),
			fv1.FUNCTION_GENERATION: strconv.FormatInt(gen, 10),
		}
		if requireServed {
			sel[fv1.SERVED_LABEL] = fv1.SERVED_VALUE
		}
		pods, err := f.KubeClient().CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
			LabelSelector: labels.Set(sel).AsSelector().String(),
		})
		if err != nil {
			return nil, err
		}
		return utils.ReadyAndRunningPodsFilter(pods), nil
	}

	// assertVersionPodEventually polls until at least one served pod for
	// (gen, wantVersion) exists and carries the expected version label. Used
	// once per version below — factored out so the two checks (identical but
	// for their generation/version/name) can't drift apart.
	assertVersionPodEventually := func(gen int64, wantVersion string) {
		t.Helper()
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			pods, err := podsForGeneration(gen, true)
			if assert.NoErrorf(c, err, "list pods (gen=%d)", gen) && assert.NotEmptyf(c, pods, "expected at least one pod (gen=%d)", gen) {
				assert.Equalf(c, wantVersion, pods[0].Labels[fv1.FUNCTION_VERSION], "pod %s missing/incorrect %s label", pods[0].Name, fv1.FUNCTION_VERSION)
			}
		}, 60*time.Second, 2*time.Second)
	}
	assertVersionPodEventually(v1.Spec.FunctionGeneration, v1Name)
	assertVersionPodEventually(v2.Spec.FunctionGeneration, v2Name)

	// --- warm-retention leg: alias v1, starve it of traffic, assert it survives ---
	out := ns.CLICaptureStdout(t, ctx, "alias", "create",
		"--name", aliasName, "--function", fnName, "--version", v1Name)
	assert.Contains(t, out, aliasName)

	// touchV2 refreshes v2's pool-cache Atime via /v2/tapServices -- the
	// router's real RFC-0002 keep-alive path for a pod it is holding through
	// the EndpointSlice index rather than a fresh getServiceForFunction call
	// per request. Unlike specializeVersion this never touches
	// PoolCache.activeRequests (TouchByAddress matches purely on address), so
	// it can be called on every tick with no release needed.
	touchV2 := func() {
		t.Helper()
		tctx, tcancel := context.WithTimeout(ctx, 30*time.Second)
		defer tcancel()
		status, err := postTapServices(tctx, f, []client.TapServiceRequest{{
			FnMetadata:     versioning.VersionedFunction(live, v2).ObjectMeta,
			FnExecutorType: fv1.ExecutorTypePoolmgr,
			ServiceURL:     addr2,
		}})
		assert.NoErrorf(t, err, "POST /v2/tapServices for v2 (%s)", addr2)
		assert.Equalf(t, http.StatusOK, status, "tapServices for v2 (%s)", addr2)
	}

	// Only v2 gets traffic for windowMultiplier x the idle timeout. v1's warm
	// pod's Atime is never refreshed (untouched since the initial
	// mustSpecialize(v1) call above), so absent alias-retention the idle
	// reaper would have drained it many ticks ago (reap tick default 5s,
	// idleTimeoutSeconds well under the window). Polling the pod list on
	// every touch turns this into a continuous "never reaped" assertion
	// across the whole window, not just a single point-in-time check.
	window := time.Duration(idleTimeoutSeconds*windowMultiplier) * time.Second
	touchInterval := 5 * time.Second
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		time.Sleep(touchInterval)
		touchV2()
		p1, err := podsForGeneration(v1.Spec.FunctionGeneration, true)
		assert.NoErrorf(t, err, "list v1 pods during retention window")
		assert.NotEmptyf(t, p1, "v1 pod (gen=%d) reaped too early despite alias %q retaining it (%s remaining)",
			v1.Spec.FunctionGeneration, aliasName, time.Until(deadline).Round(time.Second))
	}

	// Strong final check before moving the alias.
	finalV1Pods, err := podsForGeneration(v1.Spec.FunctionGeneration, true)
	require.NoError(t, err, "list v1 pods at end of retention window")
	require.NotEmptyf(t, finalV1Pods, "v1 pod (gen=%d) must survive the alias-retained warm window", v1.Spec.FunctionGeneration)

	// --- delete the alias; v1 is no longer retained and eventually drains ---
	out = ns.CLICaptureStdout(t, ctx, "alias", "delete", "--name", aliasName)
	assert.Contains(t, out, aliasName)

	// requireServed=false: the reaper unlabels fission.io/served before the
	// delayed pod delete (RFC-0002 drain grace), so waiting for the pod
	// object itself to disappear -- not just the label -- proves the full
	// drain, not merely the EndpointSlice-visibility step.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, err := podsForGeneration(v1.Spec.FunctionGeneration, false)
		if !assert.NoErrorf(c, err, "list v1 pods (gen=%d) after alias delete", v1.Spec.FunctionGeneration) {
			return
		}
		assert.Emptyf(c, pods, "v1 pods (gen=%d) should be fully drained once the alias no longer retains them (still have %d)",
			v1.Spec.FunctionGeneration, len(pods))
	}, 4*time.Minute, 5*time.Second)
}

// postJSON POSTs a JSON-marshaled body to path on the executor's HTTP API and
// returns (status, response body, error). The shared low-level call behind
// postGetServiceForFunction, postUnTapService, and postTapServices below.
func postJSON(ctx context.Context, f *framework.Framework, path string, payload any) (int, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.ExecutorBaseURL()+path, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.ExecutorClient().Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(respBody), nil
}

// postGetServiceForFunction POSTs a versioned Function projection to the
// executor's direct-specialize API and returns (status, address, error). A
// successful call increments the pool cache entry's activeRequests (see
// releaseVersion in TestVersionedSpecialize) — callers must release it via
// postUnTapService once done with the address.
func postGetServiceForFunction(ctx context.Context, f *framework.Framework, fn *fv1.Function) (int, string, error) {
	return postJSON(ctx, f, "/v2/getServiceForFunction", fn)
}

// postUnTapService POSTs /v2/unTapService, releasing the pool cache
// activeRequests slot a prior postGetServiceForFunction call reserved for
// (fnMeta.UID, fnMeta.Generation) at address. Returns (status, error).
func postUnTapService(ctx context.Context, f *framework.Framework, fnMeta metav1.ObjectMeta, address string) (int, error) {
	status, _, err := postJSON(ctx, f, "/v2/unTapService", client.TapServiceRequest{
		FnMetadata:     fnMeta,
		FnExecutorType: fv1.ExecutorTypePoolmgr,
		ServiceURL:     address,
	})
	return status, err
}

// postTapServices POSTs /v2/tapServices, refreshing the pool cache Atime of
// each request's address by address lookup alone -- it never touches
// activeRequests, so (unlike postGetServiceForFunction) it needs no release.
// Returns (status, error).
func postTapServices(ctx context.Context, f *framework.Framework, reqs []client.TapServiceRequest) (int, error) {
	status, _, err := postJSON(ctx, f, "/v2/tapServices", reqs)
	return status, err
}
