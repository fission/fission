// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/metrics"
)

// setupRouterMeter installs a MeterProvider backed by a fresh Prometheus
// registry (mirrors pkg/utils/metrics's own setupMeter test helper): the
// router's package-level counters/histogram are created against the global
// MeterProvider delegate at package-init time, so a test-installed provider
// retroactively wires them to this registry.
func setupRouterMeter(t *testing.T) *prometheus.Registry {
	t.Helper()
	reg := prometheus.NewRegistry()
	mp, err := metrics.NewMeterProvider(resource.Default(), reg, true)
	require.NoError(t, err)
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { _ = mp.Shutdown(t.Context()) })
	return reg
}

func findRouterMetric(fam *dto.MetricFamily, want map[string]string) *dto.Metric {
	for _, m := range fam.GetMetric() {
		labels := map[string]string{}
		for _, l := range m.GetLabel() {
			labels[l.GetName()] = l.GetValue()
		}
		match := true
		for k, v := range want {
			if labels[k] != v {
				match = false
				break
			}
		}
		if match {
			return m
		}
	}
	return nil
}

// TestFunctionCallAttrsCacheKeyIncludesVersion pins the attr-cache key shape
// (metrics.go: [6]string{namespace,name,version,path,method,code}): two
// calls differing only in version must occupy DISTINCT cache entries, so the
// version label actually reaches the exposed series independently of
// namespace/name/path/method/code, while an identical repeat call hits the
// existing entry rather than growing the cache.
func TestFunctionCallAttrsCacheKeyIncludesVersion(t *testing.T) {
	countEntries := func() int {
		n := 0
		functionCallAttrsCache.Range(func(any, any) bool { n++; return true })
		return n
	}

	before := countEntries()
	_ = functionCallAttrs("cache-key-test-ns", "cache-key-test-fn", "v1", "/p", http.MethodGet, 200)
	afterV1 := countEntries()
	assert.Equal(t, before+1, afterV1, "a new version must add a new cache entry")

	_ = functionCallAttrs("cache-key-test-ns", "cache-key-test-fn", "v2", "/p", http.MethodGet, 200)
	afterV2 := countEntries()
	assert.Equal(t, afterV1+1, afterV2, "a different version must NOT reuse another version's cache entry")

	// Identical repeat: memoized, no growth.
	_ = functionCallAttrs("cache-key-test-ns", "cache-key-test-fn", "v1", "/p", http.MethodGet, 200)
	assert.Equal(t, afterV2, countEntries(), "an identical (including version) call must hit the cache")
}

// TestFunctionCallMetrics_VersionLabel drives collectFunctionMetric for both
// a versioned and an unversioned function against ONE shared registry (the
// router's counters/histogram bind to the global MeterProvider the first
// time one is installed in this test binary; a second setupRouterMeter call
// in a later test would silently keep recording into the FIRST registry, so
// both cases share one installation here rather than risking that trap) and
// asserts:
//   - the versioned invocation's fission_function_calls_total /
//     fission_function_errors_total / fission_function_overhead_seconds
//     series all carry function_version set to the label's value.
//   - the unversioned invocation's series carries an EMPTY (not missing or
//     garbage) function_version value -- Prometheus TSDB series identity
//     treats empty and absent as the same series, so this does not create a
//     new time series for existing unversioned dashboards/alerts.
func TestFunctionCallMetrics_VersionLabel(t *testing.T) {
	reg := setupRouterMeter(t)

	call := func(fn *fv1.Function, path string, status int) {
		fh := functionHandler{function: fn}
		backend, _ := url.Parse("http://10.0.0.9:8888")
		req := httptest.NewRequest(http.MethodGet, "http://x"+path, nil)
		resp := &http.Response{StatusCode: status}
		fh.collectFunctionMetric(time.Now(), &RetryingRoundTripper{serviceURL: backend}, req, resp)
	}

	versioned := &fv1.Function{ObjectMeta: metav1.ObjectMeta{
		Name: "hello", Namespace: "default",
		Labels: map[string]string{fv1.FUNCTION_VERSION: "hello-v1"},
	}}
	call(versioned, "/versioned-metric-test", 500) // >=400 so functionCallErrors also records

	unversioned := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "classic", Namespace: "default"}}
	call(unversioned, "/unversioned-metric-test", 200)

	families, err := reg.Gather()
	require.NoError(t, err)
	byName := map[string]*dto.MetricFamily{}
	for _, f := range families {
		byName[f.GetName()] = f
	}

	t.Run("versioned invocation carries function_version", func(t *testing.T) {
		want := map[string]string{
			"function_namespace": "default",
			"function_name":      "hello",
			"function_version":   "hello-v1",
		}
		for _, name := range []string{"fission_function_calls_total", "fission_function_errors_total", "fission_function_overhead_seconds"} {
			fam := byName[name]
			require.NotNil(t, fam, "%s must be exposed", name)
			m := findRouterMetric(fam, want)
			require.NotNil(t, m, "%s must carry function_version=hello-v1 (have families: %+v)", name, fam.GetMetric())
		}
	})

	t.Run("unversioned invocation carries an empty function_version", func(t *testing.T) {
		fam := byName["fission_function_calls_total"]
		require.NotNil(t, fam)
		m := findRouterMetric(fam, map[string]string{
			"function_namespace": "default",
			"function_name":      "classic",
		})
		require.NotNil(t, m)
		idx := labelIndex(m, "function_version")
		require.GreaterOrEqual(t, idx, 0, "function_version label must be present (empty, not absent)")
		assert.Equal(t, "", m.GetLabel()[idx].GetValue())
	})
}

// labelIndex finds the index of a label by name on a gathered metric.
func labelIndex(m *dto.Metric, name string) int {
	for i, l := range m.GetLabel() {
		if l.GetName() == name {
			return i
		}
	}
	return -1
}
