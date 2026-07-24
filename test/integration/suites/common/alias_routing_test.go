// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// RFC-0025 Phase 3 Task 8: the live integration battery proving the RFC's
// headline alias-routing behaviors end to end against a real cluster --
// invoke-by-alias (public + internal listener), sub-second alias repoint,
// weighted traffic split, rollback warmth, sticky x weighted determinism,
// keyed-state continuity across a rollback, async invocation's enqueue-time
// version pin, the async deliverer's GC'd-version fallback, and the router's
// zero-drift guarantee around alias repoints. Companion to
// versioned_specialize_test.go (phase 2, direct-executor specialize) and
// functionversion_test.go (phase 1, publish/alias CRUD + webhook guards) --
// this file is the first to route actual HTTP traffic through an alias.
package common_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/asyncinvoke"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/test/integration/framework"
)

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// createAliasRoute creates an HTTPTrigger whose FunctionReference resolves
// through a FunctionAlias (fv1.FunctionReference.Alias) rather than the live
// Function directly. `fission route create` has no --alias flag yet -- Phase
// 3 added the CRD field, CEL rules, and router/webhook support for it (see
// pkg/apis/core/v1/types.go's FunctionReference.Alias doc comment), but not a
// CLI surface (pkg/fission-cli/cmd/httptrigger/create.go's setHtFunctionRef
// only builds Type name / FunctionWeights) -- so this builds and creates the
// CRD directly through the typed client, the same way
// functionversion_test.go/versioned_specialize_test.go reach past the CLI
// for assertions/setup it has no flag for.
func createAliasRoute(t *testing.T, ctx context.Context, ns *framework.TestNamespace, name, urlPath, fnName, aliasName string, methods ...string) {
	t.Helper()
	fc := ns.Framework().FissionClient().CoreV1()
	prefix := ""
	trigger := &fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name},
		Spec: fv1.HTTPTriggerSpec{
			RelativeURL: urlPath,
			Methods:     methods,
			Prefix:      &prefix,
			FunctionReference: fv1.FunctionReference{
				Type:  fv1.FunctionReferenceTypeFunctionName,
				Name:  fnName,
				Alias: aliasName,
			},
		},
	}
	_, err := fc.HTTPTriggers(ns.Name).Create(ctx, trigger, metav1.CreateOptions{})
	require.NoErrorf(t, err, "create alias-based HTTPTrigger %q", name)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		_ = fc.HTTPTriggers(ns.Name).Delete(cctx, name, metav1.DeleteOptions{})
	})
}

// aliasFixture is the ~25-line env/fn/publish-v1/update/publish-v2/naming/
// cleanup skeleton every TestAlias* test in this file starts from: it derives
// the deterministic per-test resource names from a short tag + ns.ID (the
// same naming scheme every one of these tests already used ad hoc), and
// registers t.Cleanup for the four CRDs these tests create directly through
// the typed client -- HTTPTrigger, FunctionAlias, and both FunctionVersions.
// (CreateFunction/CreateEnv register their own cleanup via ns; these four
// don't have an ns-owned equivalent because they aren't created through ns's
// CLI helpers.) Per-test variation -- function options, what the v2 update
// actually changes, the alias's weight/secondary-version, the route's
// methods, the alias name's own prefix -- is caller-applied through
// publishTwoVersions/createAlias/createRoute's own parameters rather than
// baked in here.
type aliasFixture struct {
	ns *framework.TestNamespace

	EnvName, FnName, AliasName, RouteName, RoutePath string
	V1Name, V2Name                                   string
}

// newAliasFixture creates ns, computes the fixture's resource names --
// env/fn/route named "<kind>-<tag>-<ns.ID>", route path "/<tag>-<ns.ID>",
// alias named "<aliasPrefix>-<ns.ID>", FunctionVersion names
// "<FnName>-v1"/"<FnName>-v2" -- and registers their cleanup.
func newAliasFixture(t *testing.T, f *framework.Framework, tag, aliasPrefix string) *aliasFixture {
	t.Helper()
	ns := f.NewTestNamespace(t)
	fc := f.FissionClient().CoreV1()
	af := &aliasFixture{
		ns:        ns,
		EnvName:   "nodejs-" + tag + "-" + ns.ID,
		FnName:    "fn-" + tag + "-" + ns.ID,
		AliasName: aliasPrefix + "-" + ns.ID,
		RouteName: "route-" + tag + "-" + ns.ID,
		RoutePath: "/" + tag + "-" + ns.ID,
	}
	af.V1Name = af.FnName + "-v1"
	af.V2Name = af.FnName + "-v2"
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		_ = fc.HTTPTriggers(ns.Name).Delete(cctx, af.RouteName, metav1.DeleteOptions{})
		_ = fc.FunctionAliases(ns.Name).Delete(cctx, af.AliasName, metav1.DeleteOptions{})
		_ = fc.FunctionVersions(ns.Name).Delete(cctx, af.V1Name, metav1.DeleteOptions{})
		_ = fc.FunctionVersions(ns.Name).Delete(cctx, af.V2Name, metav1.DeleteOptions{})
	})
	return af
}

// publishTwoVersions creates the environment and function (fnOpts.Name/Env
// are overwritten with the fixture's EnvName/FnName; every other
// framework.FunctionOptions field is the caller's to fill in) and publishes
// it as v1, then runs `fn update --name <FnName> <updateArgs...>` (e.g.
// "--code", path, or "--fntimeout", "45") and publishes again as v2.
func (af *aliasFixture) publishTwoVersions(t *testing.T, ctx context.Context, image string, fnOpts framework.FunctionOptions, updateArgs ...string) {
	t.Helper()
	fnOpts.Name = af.FnName
	fnOpts.Env = af.EnvName
	af.ns.CreateEnv(t, ctx, framework.EnvOptions{Name: af.EnvName, Image: image})
	af.ns.CreateFunction(t, ctx, fnOpts)
	af.ns.WaitForFunction(t, ctx, af.FnName)
	af.ns.CLI(t, ctx, "fn", "publish", "--name", af.FnName, "--wait")
	af.ns.CLI(t, ctx, append([]string{"fn", "update", "--name", af.FnName}, updateArgs...)...)
	af.ns.CLI(t, ctx, "fn", "publish", "--name", af.FnName, "--wait")
}

// createAlias runs `alias create --name <AliasName> --function <FnName>
// --version <version>` plus any extraArgs (e.g. "--weight", "50",
// "--secondary-version", af.V2Name) and returns the captured stdout.
func (af *aliasFixture) createAlias(t *testing.T, ctx context.Context, version string, extraArgs ...string) string {
	t.Helper()
	args := append([]string{"alias", "create", "--name", af.AliasName, "--function", af.FnName, "--version", version}, extraArgs...)
	return af.ns.CLICaptureStdout(t, ctx, args...)
}

// createRoute creates the alias-routed HTTPTrigger (see createAliasRoute)
// using the fixture's RouteName/RoutePath/FnName/AliasName.
func (af *aliasFixture) createRoute(t *testing.T, ctx context.Context, methods ...string) {
	t.Helper()
	createAliasRoute(t, ctx, af.ns, af.RouteName, af.RoutePath, af.FnName, af.AliasName, methods...)
}

// writeNodeStatus writes a Node.js function that logs `marker` via
// console.log (so an async invocation -- whose HTTP response is never
// returned to the caller -- can still be observed via FunctionLogs) and then
// returns `body` with HTTP `status`. Extends writeNodeReturning
// (function_update_test.go, always 200) with an explicit status and a log
// line.
func writeNodeStatus(t *testing.T, fileName string, status int, marker, body string) string {
	t.Helper()
	src := fmt.Sprintf(
		"module.exports = function(context, callback) {\n  console.log(%s);\n  callback(%d, %s);\n};\n",
		jsString(marker), status, jsString(body))
	p := filepath.Join(t.TempDir(), fileName+".js")
	require.NoErrorf(t, os.WriteFile(p, []byte(src), 0o644), "write %q", p)
	return p
}

// writeNodeStatusDelayed is writeNodeStatus with the callback deferred by
// `delay`: the function logs `marker` immediately, then answers `status`/`body`
// only after the delay elapses. TestAliasAsyncGCFallback uses it to make a
// failing delivery attempt OCCUPY a known-length window (the platform's async
// attempt budget is fixed at 3 — see fv1.MaxAsyncAttempts — so the test cannot
// buy timing headroom with a bigger retry budget; it buys it with a slow
// failure instead).
func writeNodeStatusDelayed(t *testing.T, fileName string, delay time.Duration, status int, marker, body string) string {
	t.Helper()
	src := fmt.Sprintf(
		"module.exports = function(context, callback) {\n  console.log(%s);\n  setTimeout(function() { callback(%d, %s); }, %d);\n};\n",
		jsString(marker), status, jsString(body), delay.Milliseconds())
	p := filepath.Join(t.TempDir(), fileName+".js")
	require.NoErrorf(t, os.WriteFile(p, []byte(src), 0o644), "write %q", p)
	return p
}

// writeNodeKVFixture writes a Node.js function implementing a tiny
// unconditional key-value contract against the RFC-0023 keyed-state API,
// mirroring testdata/nodejs/state/counter.js's get -> CAS-write retry loop
// (same injected env var / token file / header contract, proven working
// there) but writing an arbitrary caller-supplied value instead of
// incrementing a counter: GET returns the current value of the fixed key
// "shared" (404 if unset), POST ?value=<v> CAS-writes it. Used to prove
// state continuity across an alias rollback: the keyspace is a property of
// the Function, not any one FunctionVersion, so a value written while v2
// served must still be visible once the alias rolls back to v1.
func writeNodeKVFixture(t *testing.T) string {
	t.Helper()
	const src = `const fs = require('fs');
function creds() {
  return JSON.parse(fs.readFileSync(process.env.FISSION_STATE_TOKEN_PATH, 'utf8'));
}
module.exports = async function (context) {
  const base = process.env.FISSION_STATE_URL;
  const c = creds();
  const hdrs = {
    'Authorization': 'Bearer ' + c.token,
    'X-Fission-State-Namespace': c.namespace,
    'X-Fission-State-Keyspace': c.keyspace,
  };
  if (context.request.method === 'POST') {
    const value = (context.request.query && context.request.query.value) || '';
    for (let attempt = 0; attempt < 20; attempt++) {
      const g = await fetch(base + '/v1/state/shared', { headers: hdrs });
      let ver = 0;
      if (g.status === 200) {
        ver = parseInt(g.headers.get('x-fission-state-version'), 10);
      } else if (g.status !== 404) {
        return { status: 500, body: 'get failed: ' + g.status };
      }
      const r = await fetch(base + '/v1/state/shared/cas', {
        method: 'POST',
        headers: Object.assign({}, hdrs, { 'Content-Type': 'application/json' }),
        body: JSON.stringify({ expectVersion: ver, value: Buffer.from(value).toString('base64') }),
      });
      if (r.status === 204) {
        return { status: 200, body: value };
      }
      if (r.status !== 412) {
        return { status: 500, body: 'cas failed: ' + r.status };
      }
    }
    return { status: 500, body: 'cas retries exhausted' };
  }
  const g = await fetch(base + '/v1/state/shared', { headers: hdrs });
  if (g.status === 404) {
    return { status: 404, body: 'not-found' };
  }
  if (g.status !== 200) {
    return { status: 500, body: 'get failed: ' + g.status };
  }
  return { status: 200, body: await g.text() };
};
`
	p := filepath.Join(t.TempDir(), "kv.js")
	require.NoError(t, os.WriteFile(p, []byte(src), 0o644), "write kv.js")
	return p
}

// requireAsyncInvocationEnabled skips the test unless the router runs with
// ASYNC_INVOCATION_ENABLED=true. Mirrors
// test/integration/suites/serial/async_invocation_test.go's
// requireAsyncEnabled and function_test_test.go's TestFunctionTestCLIAsync
// inline check (both unexported/local to their own files), duplicated here
// rather than reaching across a package boundary for a five-line check.
func requireAsyncInvocationEnabled(t *testing.T, ctx context.Context, f *framework.Framework) {
	t.Helper()
	dep, err := f.KubeClient().AppsV1().Deployments(f.FissionNamespace()).Get(ctx, "router", metav1.GetOptions{})
	require.NoError(t, err)
	for _, c := range dep.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == "ASYNC_INVOCATION_ENABLED" && e.Value == "true" {
				return
			}
		}
	}
	t.Skip("async invocation is not enabled on the router (ASYNC_INVOCATION_ENABLED != true); skipping")
}

// asyncPostAlias fires an X-Fission-Invoke-Mode: async POST at an
// alias-routed HTTPTrigger and returns the 202 status and invocation id.
func asyncPostAlias(t *testing.T, ctx context.Context, f *framework.Framework, routePath, dedupKey string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Router(t).BaseURL()+routePath, strings.NewReader("payload"))
	require.NoError(t, err)
	req.Header.Set(asyncinvoke.HeaderInvokeMode, asyncinvoke.InvokeModeAsync)
	if dedupKey != "" {
		req.Header.Set(asyncinvoke.HeaderDedupKey, dedupKey)
	}
	resp, err := f.HTTPClient().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, resp.Header.Get(asyncinvoke.HeaderInvocationID)
}

// servedPodNameEventually polls until exactly a ready+served pod exists for
// (uid, generation) and returns its name. Mirrors versioned_specialize_test.go's
// podsForGeneration/assertVersionPodEventually (closures local to that test)
// but standalone, and returns the name (not just presence) so callers can
// assert pod IDENTITY is unchanged across an operation (no re-specialization).
func servedPodNameEventually(t *testing.T, ctx context.Context, f *framework.Framework, ns *framework.TestNamespace, uid types.UID, gen int64) string {
	t.Helper()
	sel := labels.Set(map[string]string{
		fv1.FUNCTION_UID:        string(uid),
		fv1.FUNCTION_GENERATION: strconv.FormatInt(gen, 10),
		fv1.SERVED_LABEL:        fv1.SERVED_VALUE,
	}).AsSelector().String()
	var podName string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		pods, err := f.KubeClient().CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: sel})
		if !assert.NoErrorf(c, err, "list pods (gen=%d)", gen) {
			return
		}
		ready := utils.ReadyAndRunningPodsFilter(pods)
		if !assert.NotEmptyf(c, ready, "no served+ready pod yet for generation %d", gen) {
			return
		}
		podName = ready[0].Name
	}, 60*time.Second, time.Second)
	return podName
}

// scrapeCounterSum scrapes metricName from every pod carrying label
// svc=<svc> in the Fission control-plane namespace via the apiserver
// pod-proxy (no port-forward needed -- the same access path
// memory_soak_test.go's readResidentMemory uses for
// process_resident_memory_bytes) and sums every matching sample line,
// labeled or not.
//
// RFC-0025's two counters here (fission_router_route_resync_drift_total,
// fission_async_version_fallback_total) are OTel Int64Counters that emit NO
// Prometheus sample line until Add() is called at least once -- verified
// live against a freshly deployed router: /metrics lists
// fission_router_routes and fission_router_mux_rebuilds_total (both
// instruments with at least one recorded point) but omits
// resync_drift/version_fallback entirely until something increments them.
// So "absent from the scrape" and "zero" are the same observable state for
// these two counters, and scrapeCounterSum returns 0 rather than failing
// when no line matches, instead of requiring a match the way
// memory_soak_test.go's parseMetric does for an always-present gauge.
func scrapeCounterSum(t *testing.T, ctx context.Context, f *framework.Framework, svc, metricName string) float64 {
	t.Helper()
	pods, err := f.KubeClient().CoreV1().Pods(f.FissionNamespace()).List(ctx, metav1.ListOptions{LabelSelector: "svc=" + svc})
	require.NoErrorf(t, err, "listing %s pods", svc)
	require.NotEmptyf(t, pods.Items, "no %s pod found in namespace %s", svc, f.FissionNamespace())
	var total float64
	for _, p := range pods.Items {
		raw, err := f.KubeClient().CoreV1().Pods(f.FissionNamespace()).ProxyGet("http", p.Name, "8080", "/metrics", nil).DoRaw(ctx)
		require.NoErrorf(t, err, "scraping /metrics from %s pod %s", svc, p.Name)
		total += sumMetricLines(raw, metricName)
	}
	return total
}

// sumMetricLines sums every Prometheus exposition line for `name`, labeled
// or not (both "foo 3" and `foo{a="b"} 3` match), skipping comment lines.
// Unlike memory_soak_test.go's parseMetric, which returns the first match
// for a metric callers know by construction is unlabeled, this sums every
// label combination -- needed here because callers only know these two
// counters are unlabeled by reading their registration site, not by
// contract.
func sumMetricLines(raw []byte, name string) float64 {
	var total float64
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		rest, ok := strings.CutPrefix(line, name)
		if !ok {
			continue
		}
		if len(rest) == 0 || (rest[0] != ' ' && rest[0] != '{') {
			continue // matched a longer metric name sharing this prefix
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		total += v
	}
	return total
}

// ---------------------------------------------------------------------------
// 1, 2, 8 (part 1): invoke by alias (public + internal), repoint latency,
// zero route-resync drift around a routine repoint
// ---------------------------------------------------------------------------

// TestAliasInvokeAndRepoint proves RFC-0025's headline behavior: an
// HTTPTrigger routed through a FunctionAlias (fv1.FunctionReference.Alias)
// serves the alias's currently-resolved version over both the router's
// public listener (the HTTPTrigger path) and its internal listener (the
// bare `:<alias>` route every FunctionAlias auto-materializes independent of
// any HTTPTrigger -- see pkg/router/reconciler_alias.go's
// functionAliasReconciler), and that repointing the alias converges fast.
// The RFC's target is sub-second; this asserts a CI-generous <3s hard bound
// and logs the actual elapsed time so real convergence latency is visible in
// the test log without making CI timing-flaky.
//
// The same repoint doubles as the "before"/"after" bracket for the
// zero-drift gate: fission_router_route_resync_drift_total must not move
// for a routine alias repoint -- a nonzero delta means the router's
// incremental route table (RFC-0013) missed a watch event and had to
// self-heal via the periodic resync, which should never happen in healthy
// operation regardless of what else is running concurrently in the suite.
func TestAliasInvokeAndRepoint(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)
	af := newAliasFixture(t, f, "aliasrt", "prod")
	af.publishTwoVersions(t, ctx, image,
		framework.FunctionOptions{Code: writeNodeReturning(t, "v1", "alias-marker-v1\n")},
		"--code", writeNodeReturning(t, "v2", "alias-marker-v2\n"))

	out := af.createAlias(t, ctx, af.V1Name)
	assert.Contains(t, out, af.AliasName)

	af.createRoute(t, ctx, http.MethodGet)

	t.Run("public_listener_serves_resolved_version", func(t *testing.T) {
		body := f.Router(t).GetEventually(t, ctx, af.RoutePath, framework.BodyContains("alias-marker-v1"))
		assert.Contains(t, body, "alias-marker-v1")
	})

	t.Run("internal_listener_serves_resolved_version", func(t *testing.T) {
		internalPath := "/fission-function/" + af.FnName + ":" + af.AliasName
		body := f.Router(t).GetEventually(t, ctx, internalPath, framework.BodyContains("alias-marker-v1"))
		assert.Contains(t, body, "alias-marker-v1")
	})

	t.Run("repoint_latency_and_zero_drift", func(t *testing.T) {
		driftBefore := scrapeCounterSum(t, ctx, f, "router", "fission_router_route_resync_drift_total")

		af.ns.CLICaptureStdout(t, ctx, "alias", "update", "--name", af.AliasName, "--version", af.V2Name)
		ackTime := time.Now() // the alias PATCH ack -- elapsed is measured from here

		require.EventuallyWithT(t, func(c *assert.CollectT) {
			status, body, err := f.Router(t).Get(ctx, af.RoutePath)
			if !assert.NoError(c, err) {
				return
			}
			assert.Equal(c, http.StatusOK, status)
			assert.Contains(c, body, "alias-marker-v2")
		}, 30*time.Second, 50*time.Millisecond)
		elapsed := time.Since(ackTime)

		t.Logf("alias repoint: public-route convergence in %s (RFC-0025 target <1s; CI-generous hard bound <3s)", elapsed)
		assert.Lessf(t, elapsed, 3*time.Second, "alias repoint must converge well under 3s (RFC target <1s)")

		driftAfter := scrapeCounterSum(t, ctx, f, "router", "fission_router_route_resync_drift_total")
		assert.Equalf(t, driftBefore, driftAfter,
			"a routine alias repoint must not increment fission_router_route_resync_drift_total (missed watch event)")
	})
}

// ---------------------------------------------------------------------------
// 3: both-versions-traffic (anti-collapse regression)
// ---------------------------------------------------------------------------

// TestAliasWeightedTrafficSplit is RFC-0025's anti-collapse regression: a
// FunctionAlias split 50/50 between two FunctionVersions must actually serve
// BOTH of them under sustained (unkeyed) traffic, not silently collapse onto
// one side -- the historical bug class an off-by-one in the weighted pick or
// a wrong "always primary" default would produce silently and which the
// deterministic sticky-pick commit's fix note documents
// (pkg/router/canary.go's getCanaryBackend: draws now happen over [0, total)
// on both sides so even a boundary split can't starve one arm).
func TestAliasWeightedTrafficSplit(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)
	af := newAliasFixture(t, f, "aliaswt", "canary")
	af.publishTwoVersions(t, ctx, image,
		framework.FunctionOptions{Code: writeNodeReturning(t, "v1", "weighted-v1\n")},
		"--code", writeNodeReturning(t, "v2", "weighted-v2\n"))

	af.createAlias(t, ctx, af.V1Name, "--weight", "50", "--secondary-version", af.V2Name)

	af.createRoute(t, ctx, http.MethodGet)

	// Prime: wait for the route to serve before counting -- the first hits
	// may race pod specialization for whichever version happens to be
	// picked first.
	f.Router(t).GetEventually(t, ctx, af.RoutePath, func(status int, body string) bool {
		return status == http.StatusOK && (strings.Contains(body, "weighted-v1") || strings.Contains(body, "weighted-v2"))
	})

	const n = 200
	var v1Count, v2Count, otherCount int
	for i := 0; i < n; i++ {
		status, body, err := f.Router(t).Get(ctx, af.RoutePath)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		switch {
		case strings.Contains(body, "weighted-v1"):
			v1Count++
		case strings.Contains(body, "weighted-v2"):
			v2Count++
		default:
			otherCount++
		}
	}

	t.Logf("50/50 alias split over %d requests: v1=%d v2=%d other=%d", n, v1Count, v2Count, otherCount)
	assert.Zerof(t, otherCount, "every response must carry exactly one of the two version markers")
	assert.Greaterf(t, v1Count, n/5, "v1 must receive a real share of traffic (>20%%), got %d/%d", v1Count, n)
	assert.Greaterf(t, v2Count, n/5, "v2 must receive a real share of traffic (>20%%), got %d/%d", v2Count, n)
}

// ---------------------------------------------------------------------------
// 4, 7, 8 (part 2): rollback warmth, internal-route repoint after rollback,
// zero route-resync drift around a repoint+rollback
// ---------------------------------------------------------------------------

// TestAliasRollbackWarmth proves `fission fn rollback` is actually fast: it
// repoints the alias to the FunctionVersion recorded in Status.History (no
// --to given) and the version's pod -- last live moments earlier while the
// alias still referenced it -- is still there to serve the very next
// request, with no re-specialization.
//
// NOTE on the exact warmth mechanism, so the assertion below isn't read as
// claiming more than the code guarantees: pkg/executor/versionretain.View
// retains only a version currently referenced by an alias's
// Spec.Version/SecondaryVersion/Status.ResolvedVersion -- NOT
// Status.History -- so the moment the alias moves off v1 onto v2, v1's
// warm-floor retention lapses (View.Rebuild, verified by reading the
// source). What actually keeps THIS rollback fast is that v1's pod has not
// yet been REAPED (idle timeout has not elapsed) at the moment of rollback;
// once the alias moves back onto v1, retention resumes and the reaper never
// gets a chance to act. That is a real, useful, time-bounded guarantee --
// rollback shortly after a bad repoint is warm; rollback long after (past
// IdleTimeout, with the reaper having already run) is a cold start like any
// other. IdleTimeout below is set to 300s, far beyond this test's own
// wall-clock (a handful of seconds), specifically so the reaper can never
// race the assertion either way.
func TestAliasRollbackWarmth(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)
	af := newAliasFixture(t, f, "aliasrb", "prod")
	fc := f.FissionClient().CoreV1()

	af.publishTwoVersions(t, ctx, image, framework.FunctionOptions{
		Code:        writeNodeReturning(t, "v1", "rollback-marker-v1\n"),
		IdleTimeout: 300, // generous: the reaper must not fire during this test's wall clock
	}, "--code", writeNodeReturning(t, "v2", "rollback-marker-v2\n"))

	af.createAlias(t, ctx, af.V1Name)
	af.createRoute(t, ctx, http.MethodGet)

	liveFn, err := fc.Functions(af.ns.Name).Get(ctx, af.FnName, metav1.GetOptions{})
	require.NoError(t, err)
	v1, err := fc.FunctionVersions(af.ns.Name).Get(ctx, af.V1Name, metav1.GetOptions{})
	require.NoError(t, err)

	// Specialize v1 through the alias and capture the pod that served it.
	f.Router(t).GetEventually(t, ctx, af.RoutePath, framework.BodyContains("rollback-marker-v1"))
	v1PodBefore := servedPodNameEventually(t, ctx, f, af.ns, liveFn.UID, v1.Spec.FunctionGeneration)

	// Move the alias to v2 (History[last] becomes v1) but never invoke
	// through it again -- v1's pod is simply left idle, not reaped (see the
	// doc comment above on why 300s of headroom makes that safe).
	driftBefore := scrapeCounterSum(t, ctx, f, "router", "fission_router_route_resync_drift_total")
	af.ns.CLICaptureStdout(t, ctx, "alias", "update", "--name", af.AliasName, "--version", af.V2Name)
	f.Router(t).GetEventually(t, ctx, af.RoutePath, framework.BodyContains("rollback-marker-v2"))

	alias, err := fc.FunctionAliases(af.ns.Name).Get(ctx, af.AliasName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotEmptyf(t, alias.Status.History, "alias must record v1 in Status.History after the repoint")
	require.Equal(t, af.V1Name, alias.Status.History[len(alias.Status.History)-1].Version)

	// --- subtest 4: rollback warmth ---
	out := af.ns.CLICaptureStdout(t, ctx, "fn", "rollback", "--name", af.FnName, "--alias", af.AliasName, "--wait")
	assert.Contains(t, out, af.V1Name)

	body := f.Router(t).GetEventually(t, ctx, af.RoutePath, framework.BodyContains("rollback-marker-v1"))
	assert.Contains(t, body, "rollback-marker-v1")

	v1PodAfter := servedPodNameEventually(t, ctx, f, af.ns, liveFn.UID, v1.Spec.FunctionGeneration)
	assert.Equalf(t, v1PodBefore, v1PodAfter,
		"rollback must reuse the SAME v1 pod (no re-specialization) when performed within the idle window")

	driftAfter := scrapeCounterSum(t, ctx, f, "router", "fission_router_route_resync_drift_total")
	assert.Equalf(t, driftBefore, driftAfter,
		"the repoint+rollback pair must not increment fission_router_route_resync_drift_total")

	// --- subtest 7: internal-route repoint after rollback ---
	t.Run("internal_route_reflects_rollback", func(t *testing.T) {
		internalPath := "/fission-function/" + af.FnName + ":" + af.AliasName
		start := time.Now()
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			status, body, err := f.Router(t).Get(ctx, internalPath)
			if !assert.NoError(c, err) {
				return
			}
			assert.Equal(c, http.StatusOK, status)
			assert.Contains(c, body, "rollback-marker-v1")
		}, 10*time.Second, 50*time.Millisecond)
		t.Logf("internal :%s route reflected the rollback within %s of the check starting (the `fn rollback --wait` above already blocked for CRD-level resolution)", af.AliasName, time.Since(start))
	})
}

// ---------------------------------------------------------------------------
// 5: sticky stability under a weighted alias split
// ---------------------------------------------------------------------------

// TestAliasStickyStability proves the sticky x weighted deterministic pick
// (pkg/router/canary.go's getCanaryBackend with a non-empty stickyKey, added
// alongside RFC-0025's alias weighting -- see functionHandler.go's
// precomputed stickySource): for one fixed sticky key, EVERY request lands
// on the SAME version -- the FNV-64a hash of the key mod the distribution's
// total weight never changes for an unchanged distribution -- while
// different keys, in aggregate, actually distribute across both versions per
// the declared 90/10 split (never all landing on the primary by
// coincidence).
//
// This is deliberately about VERSION stability, not pod residency:
// function_state_test.go's sticky_config_serves subtest already explains why
// an end-to-end POD-residency assertion is not made against poolmgr's
// self-scheduled warm pool there (a latency optimization, not a correctness
// one -- S6). The pick this test asserts is a pure function of the key and
// the alias's current distribution, so it is exactly as flake-proof as the
// unit tests in pkg/router/sticky_weighted_pick_test.go, just exercised here
// end to end over a live cluster.
func TestAliasStickyStability(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)
	af := newAliasFixture(t, f, "aliassticky", "sticky")
	af.publishTwoVersions(t, ctx, image, framework.FunctionOptions{
		Code:  writeNodeReturning(t, "v1", "sticky-v1\n"),
		State: true, StateStickySource: "queryparam", StateStickyName: "sid",
	}, "--code", writeNodeReturning(t, "v2", "sticky-v2\n"))

	af.createAlias(t, ctx, af.V1Name, "--weight", "90", "--secondary-version", af.V2Name)
	af.createRoute(t, ctx, http.MethodGet)

	markerFor := func(body string) string {
		switch {
		case strings.Contains(body, "sticky-v1"):
			return "v1"
		case strings.Contains(body, "sticky-v2"):
			return "v2"
		default:
			return "?"
		}
	}

	f.Router(t).GetEventually(t, ctx, af.RoutePath+"?sid=warm", func(status int, body string) bool {
		return status == http.StatusOK && markerFor(body) != "?"
	})

	t.Run("fixed_key_pins_one_version", func(t *testing.T) {
		const key = "sticky-fixed-key-alpha"
		seen := map[string]int{}
		for i := 0; i < 50; i++ {
			status, body, err := f.Router(t).Get(ctx, af.RoutePath+"?sid="+key)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			seen[markerFor(body)]++
		}
		t.Logf("fixed key %q over 50 requests: %v", key, seen)
		assert.Lenf(t, seen, 1, "a fixed sticky key must resolve to exactly one version across all requests, got %v", seen)
	})

	t.Run("distinct_keys_distribute", func(t *testing.T) {
		// 60 distinct keys against a 90/10 split: P(zero v2 hits) = 0.9^60 ~
		// 0.0018 -- a <0.2% false-negative rate, generous for CI.
		const nKeys = 60
		seenVersions := map[string]int{}
		for i := 0; i < nKeys; i++ {
			key := fmt.Sprintf("sticky-distinct-key-%d", i)
			status, body, err := f.Router(t).Get(ctx, af.RoutePath+"?sid="+key)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			seenVersions[markerFor(body)]++
		}
		t.Logf("%d distinct keys over a 90/10 split: %v", nKeys, seenVersions)
		assert.Contains(t, seenVersions, "v1", "the 90%% primary must be reachable")
		assert.Contains(t, seenVersions, "v2", "the 10%% secondary must be reachable for at least one of %d distinct keys", nKeys)
	})
}

// ---------------------------------------------------------------------------
// 9: keyed-state continuity across a rollback
// ---------------------------------------------------------------------------

// TestAliasKeyedStateContinuity proves the RFC-0023 keyed-state API's
// keyspace is a property of the Function, not any one FunctionVersion: a
// value written while the alias resolves to v2 must still be visible once
// the alias rolls back to v1 -- the two versions are the same Function
// identity, and statesvc scopes storage by namespace+keyspace, neither of
// which the alias's currently-resolved version affects.
func TestAliasKeyedStateContinuity(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	stateSvcReachableOrSkip(t, ctx, f)
	if mode := f.TenancyMode(t, ctx); mode != "static" {
		t.Skipf("function state is static-tenancy only for now; tenancy mode is %q (per-namespace state key is a follow-up)", mode)
	}
	image := f.Images().RequireNode(t)
	af := newAliasFixture(t, f, "aliasstate", "prod")
	af.publishTwoVersions(t, ctx, image,
		framework.FunctionOptions{Code: writeNodeKVFixture(t), State: true},
		// v2's code is byte-identical to v1's (the fixture carries no version
		// marker -- state continuity is the point here, not code differences),
		// but publish only mints a new version when the live spec actually
		// changed. A distinct FnTimeout is a runtime-affecting, otherwise-inert
		// change that mints v2 without touching the fixture.
		"--fntimeout", "45")

	af.createAlias(t, ctx, af.V2Name)
	af.createRoute(t, ctx, http.MethodGet, http.MethodPost)

	// --- write under v2 ---
	const value = "continuity-42"
	var writeStatus int
	var writeBody string
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var err error
		writeStatus, writeBody, err = f.Router(t).Post(ctx, af.RoutePath+"?value="+value, "", nil)
		if !assert.NoError(c, err) {
			return
		}
		assert.Equal(c, http.StatusOK, writeStatus)
	}, 2*time.Minute, 2*time.Second)
	require.Equalf(t, http.StatusOK, writeStatus, "state write via v2 failed: %s", writeBody)
	require.Equal(t, value, writeBody)

	// --- rollback to v1 ---
	out := af.ns.CLICaptureStdout(t, ctx, "fn", "rollback", "--name", af.FnName, "--alias", af.AliasName, "--to", af.V1Name, "--wait")
	assert.Contains(t, out, af.V1Name)

	// --- read under v1: the value written under v2 must still be visible ---
	body := f.Router(t).GetEventually(t, ctx, af.RoutePath, framework.BodyContains(value))
	assert.Equal(t, value, body)
}

// ---------------------------------------------------------------------------
// 6: async invocation's enqueue-time version pin
// ---------------------------------------------------------------------------

// TestAliasAsyncVersionPin proves RFC-0025 Task 5's async version pin
// (asyncinvoke.Envelope.FunctionVersion): an async invocation enqueued while
// an alias resolves to v1 is delivered to v1 even if the alias moves to v2
// before delivery happens -- the version is stamped into the envelope at
// ENQUEUE time (pkg/router/async.go's handle()) and never re-resolved.
//
// This deliberately does NOT try to win a race against the dispatcher's
// (typically sub-second) first delivery attempt: the alias move happens
// right after the 202 response regardless of whether delivery already
// completed, because the property under test -- "delivery targets the
// enqueue-time version, not whatever the alias points to by the time
// delivery actually runs" -- holds identically either way. Racing the
// dispatcher's timing would only add flakiness without strengthening the
// assertion (the RFC-suggested "rollback exactly between enqueue and
// delivery" framing is a stronger-looking but strictly weaker test: it only
// proves the pin holds in the specific case where it wins that race).
func TestAliasAsyncVersionPin(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	requireAsyncInvocationEnabled(t, ctx, f)
	image := f.Images().RequireNode(t)
	af := newAliasFixture(t, f, "aliasasync", "prod")

	// This test's alias is created between v1's publish and v2's -- deliberately
	// unlike publishTwoVersions's bundled v1+v2 sequence -- so the enqueue below
	// races an alias that has only ever pointed at v1. See the doc comment above
	// for why the timing matters.
	af.ns.CreateEnv(t, ctx, framework.EnvOptions{Name: af.EnvName, Image: image})
	af.ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: af.FnName, Env: af.EnvName, ExecutorType: "poolmgr",
		Code: writeNodeStatus(t, "v1", http.StatusOK, "async-pin-v1", "async-pin-v1\n"),
	})
	af.ns.WaitForFunction(t, ctx, af.FnName)
	af.ns.CLI(t, ctx, "fn", "publish", "--name", af.FnName, "--wait")

	af.createAlias(t, ctx, af.V1Name)
	af.createRoute(t, ctx, http.MethodPost)

	af.ns.CLI(t, ctx, "fn", "update", "--name", af.FnName, "--code", writeNodeStatus(t, "v2", http.StatusOK, "async-pin-v2", "async-pin-v2\n"))
	af.ns.CLI(t, ctx, "fn", "publish", "--name", af.FnName, "--wait")

	// Warm via a synchronous POST through the alias (still resolving to v1)
	// so the route is materialized and a pod specialized before going async.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		status, body, err := f.Router(t).Post(ctx, af.RoutePath, "", nil)
		if !assert.NoError(c, err) {
			return
		}
		assert.Equal(c, http.StatusOK, status)
		assert.Contains(c, body, "async-pin-v1")
	}, 2*time.Minute, 2*time.Second)

	v1Baseline := strings.Count(af.ns.FunctionLogs(t, ctx, af.FnName), "async-pin-v1")
	v2Baseline := strings.Count(af.ns.FunctionLogs(t, ctx, af.FnName), "async-pin-v2")

	status, invocationID := asyncPostAlias(t, ctx, f, af.RoutePath, "alias-async-pin-"+af.ns.ID)
	require.Equal(t, http.StatusAccepted, status)
	require.NotEmpty(t, invocationID, "202 must carry an X-Fission-Invocation-Id")

	// Move the alias to v2 right after enqueue -- see the doc comment above
	// on why racing the dispatcher's delivery timing is neither needed nor
	// desirable here.
	af.ns.CLICaptureStdout(t, ctx, "alias", "update", "--name", af.AliasName, "--version", af.V2Name)

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		logs, err := af.ns.FunctionLogsE(t, ctx, af.FnName)
		if !assert.NoError(c, err) {
			return
		}
		assert.Greaterf(c, strings.Count(logs, "async-pin-v1"), v1Baseline,
			"async delivery must execute against the enqueue-time version (v1)")
		// 5m window: the redelivery rides RFC-0024's exponential backoff, and
		// on a contended CI runner the second attempt has been observed to
		// land past a 3m window (leg flake 2026-07-24). The poll exits as
		// soon as the count moves — the wide window only bounds the worst case.
	}, 5*time.Minute, 5*time.Second)

	finalV2 := strings.Count(af.ns.FunctionLogs(t, ctx, af.FnName), "async-pin-v2")
	assert.Equalf(t, v2Baseline, finalV2,
		"async delivery must NOT execute against v2 even though the alias moved there before/around delivery")
}

// ---------------------------------------------------------------------------
// 10: GC'd-version fallback in the async deliverer
// ---------------------------------------------------------------------------

// TestAliasAsyncGCFallback proves the async deliverer's RFC-0025 Task 5
// fallback (pkg/router/asyncinvoke/deliverer.go's httpDeliverer.Deliver):
// when an async invocation's enqueue-time-pinned FunctionVersion route has
// been garbage collected (the FunctionVersion CR deleted) by the time
// delivery is attempted, the 404 on the versioned route falls back to the
// bare-name route within the SAME delivery attempt, incrementing
// fission_async_version_fallback_total, rather than dead-lettering the
// invocation.
//
// The attempt budget CANNOT be raised for headroom: fv1.MaxAsyncAttempts
// pins per-function MaxAttempts to the statestore queue's fixed budget (3)
// at admission (a larger value would be silently capped by the store's own
// dead-lettering, so the webhook rejects it -- see validation.go). Timing
// headroom comes from a slow failure instead: v1's code answers 500 only
// after a 20s delay, so the FIRST delivery attempt -- which targets the
// still-existing versioned route -- deterministically occupies the window
// in which the two CLI operations below (repoint the alias off v1, then
// delete v1's FunctionVersion once the delete guard clears) land. Every
// subsequent attempt then 404s on the now-gone versioned route and falls
// back within the same attempt, well inside the 3-attempt budget. If the
// delete guard were ever slow enough to outlast attempt 1's window, the
// Eventually around the delete fails first with a clear message, keeping
// the failure attributable.
func TestAliasAsyncGCFallback(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	requireAsyncInvocationEnabled(t, ctx, f)
	image := f.Images().RequireNode(t)
	af := newAliasFixture(t, f, "aliasgc", "prod")
	fc := f.FissionClient().CoreV1()

	// As in TestAliasAsyncVersionPin, the alias is created between v1's
	// publish and v2's, not via publishTwoVersions's bundled sequence -- v1
	// must be the alias's only-ever target when the async POST below enqueues.
	af.ns.CreateEnv(t, ctx, framework.EnvOptions{Name: af.EnvName, Image: image})
	af.ns.CreateFunction(t, ctx, framework.FunctionOptions{
		Name: af.FnName, Env: af.EnvName, ExecutorType: "poolmgr",
		Code: writeNodeStatusDelayed(t, "v1fail", 20*time.Second, http.StatusInternalServerError, "gcfallback-v1-attempt", "boom\n"),
	})
	af.ns.WaitForFunction(t, ctx, af.FnName)
	af.ns.CLI(t, ctx, "fn", "publish", "--name", af.FnName, "--wait")

	af.createAlias(t, ctx, af.V1Name)
	af.createRoute(t, ctx, http.MethodPost)

	// The live function now succeeds -- this is the bare-name fallback
	// target. Publish it as v2 so `alias update --version v2` (needed below
	// to release the delete guard on v1) has a valid target to point at.
	af.ns.CLI(t, ctx, "fn", "update", "--name", af.FnName,
		"--code", writeNodeStatus(t, "v2ok", http.StatusOK, "gcfallback-v2-delivered", "ok\n"))
	af.ns.CLI(t, ctx, "fn", "publish", "--name", af.FnName, "--wait")

	// Warm the route through v1 -- 500 (after v1's built-in 20s delay) is
	// expected and PROVES the route round-trips to the function, which is
	// exactly what "warm" needs to mean here (v1 never succeeds by design).
	// The 3min budget absorbs cold-start + the 20s response delay per probe.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		status, _, err := f.Router(t).Post(ctx, af.RoutePath, "", nil)
		if !assert.NoError(c, err) {
			return
		}
		assert.Equal(c, http.StatusInternalServerError, status)
	}, 3*time.Minute, 2*time.Second)

	fallbackBefore := scrapeCounterSum(t, ctx, f, "router", "fission_async_version_fallback_total")
	deliveredBaseline := strings.Count(af.ns.FunctionLogs(t, ctx, af.FnName), "gcfallback-v2-delivered")

	status, _ := asyncPostAlias(t, ctx, f, af.RoutePath, "alias-gc-fallback-"+af.ns.ID)
	require.Equal(t, http.StatusAccepted, status)

	// Release the delete guard (the alias no longer references v1) and
	// delete v1's FunctionVersion -- this removes the internal `:v1` route
	// the dispatcher's next attempt targets, forcing the 404-fallback path.
	af.ns.CLICaptureStdout(t, ctx, "alias", "update", "--name", af.AliasName, "--version", af.V2Name)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		err := fc.FunctionVersions(af.ns.Name).Delete(ctx, af.V1Name, metav1.DeleteOptions{})
		assert.NoErrorf(c, err, "delete FunctionVersion %q (webhook guard should clear once the alias moved off it)", af.V1Name)
	}, 30*time.Second, time.Second)

	// The only way "gcfallback-v2-delivered" can grow now is through the
	// bare-name fallback route: v1's versioned route is gone, and no
	// synchronous request in this test hits the live function's v2 code.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		logs, err := af.ns.FunctionLogsE(t, ctx, af.FnName)
		if !assert.NoError(c, err) {
			return
		}
		assert.Greaterf(c, strings.Count(logs, "gcfallback-v2-delivered"), deliveredBaseline,
			"async delivery must fall back to the bare-name route and succeed once v1's versioned route is GC'd")
	}, 3*time.Minute, 3*time.Second)

	fallbackAfter := scrapeCounterSum(t, ctx, f, "router", "fission_async_version_fallback_total")
	assert.Greaterf(t, fallbackAfter, fallbackBefore,
		"fission_async_version_fallback_total must increment when a version-pinned async delivery 404s and falls back")
}
