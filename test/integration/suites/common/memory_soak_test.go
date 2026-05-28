// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/test/integration/framework"
)

const (
	soakEnableEnv   = "FISSION_SOAK"
	soakDurationEnv = "FISSION_SOAK_DURATION"
	fissionNS       = "fission"

	// Generous bound: post-soak RSS may legitimately grow due to warm pools,
	// GC timing, and connection pools. We only want to catch runaway growth.
	soakGrowthFactor = 2.0
	soakSlackBytes   = 128 * 1024 * 1024
)

// TestMemorySoak is an opt-in soak test for memory-leak triage. It drives
// sustained traffic at a hello function and asserts that the router and
// executor resident memory does not grow unboundedly. It is skipped unless
// FISSION_SOAK=1 so it never adds time to normal PR runs.
//
// Run locally against a kind cluster (pprof/metrics already wired by the
// kind-ci skaffold profile):
//
//	FISSION_SOAK=1 FISSION_SOAK_DURATION=3m \
//	  go test -tags=integration -run TestMemorySoak -v ./test/integration/suites/common/...
func TestMemorySoak(t *testing.T) {
	if os.Getenv(soakEnableEnv) != "1" {
		t.Skipf("soak test disabled; set %s=1 to run", soakEnableEnv)
	}

	soakDuration := 2 * time.Minute
	if v := os.Getenv(soakDurationEnv); v != "" {
		d, err := time.ParseDuration(v)
		require.NoErrorf(t, err, "invalid %s=%q", soakDurationEnv, v)
		soakDuration = d
	}

	ctx, cancel := context.WithTimeout(context.Background(), soakDuration+5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)

	ns := f.NewTestNamespace(t)
	envName := "nodejs-soak-" + ns.ID
	fnName := "nodejs-soak-" + ns.ID
	routePath := "/" + fnName

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: envName, Image: image})
	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: fnName, Env: envName, Code: codePath})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})
	ns.WaitForFunction(t, ctx, fnName)

	// Prime the route and let the warm pool specialize before the baseline read.
	f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("hello"))
	time.Sleep(10 * time.Second)

	before := map[string]float64{
		"router":   readResidentMemory(t, ctx, f, "router"),
		"executor": readResidentMemory(t, ctx, f, "executor"),
	}
	t.Logf("baseline RSS: router=%.0f bytes executor=%.0f bytes", before["router"], before["executor"])

	loadCtx, stopLoad := context.WithTimeout(ctx, soakDuration)
	defer stopLoad()
	go f.Router(t).LoadLoop(loadCtx, routePath)
	<-loadCtx.Done()

	// Allow a GC cycle to settle before measuring.
	time.Sleep(15 * time.Second)

	for _, svc := range []string{"router", "executor"} {
		after := readResidentMemory(t, ctx, f, svc)
		bound := before[svc]*soakGrowthFactor + soakSlackBytes
		t.Logf("%s RSS after soak: %.0f bytes (baseline %.0f, bound %.0f, delta %+.0f)",
			svc, after, before[svc], bound, after-before[svc])
		require.LessOrEqualf(t, after, bound,
			"%s resident memory grew beyond bound during soak (possible leak)", svc)
	}
}

// readResidentMemory scrapes process_resident_memory_bytes from a control-plane
// pod's /metrics endpoint via the API server pod proxy (no port-forward needed).
func readResidentMemory(t *testing.T, ctx context.Context, f *framework.Framework, svc string) float64 {
	t.Helper()

	pods, err := f.KubeClient().CoreV1().Pods(fissionNS).List(ctx, metav1.ListOptions{LabelSelector: "svc=" + svc})
	require.NoErrorf(t, err, "listing %s pods", svc)
	require.NotEmptyf(t, pods.Items, "no %s pod found in namespace %s", svc, fissionNS)
	podName := pods.Items[0].Name

	raw, err := f.KubeClient().CoreV1().Pods(fissionNS).
		ProxyGet("http", podName, "8080", "/metrics", nil).DoRaw(ctx)
	require.NoErrorf(t, err, "scraping /metrics from %s pod %s", svc, podName)

	val, ok := parseMetric(raw, "process_resident_memory_bytes")
	require.Truef(t, ok, "process_resident_memory_bytes not found in %s metrics", svc)
	return val
}

// parseMetric returns the value of an unlabelled prometheus metric line.
func parseMetric(raw []byte, name string) (float64, bool) {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	prefix := name + " "
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		return v, true
	}
	return 0, false
}
