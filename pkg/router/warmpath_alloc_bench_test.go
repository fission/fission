// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// These benchmarks pin the per-request allocation wins on the router warm path.
// Each "…New" benchmark exercises the shipped code; its "…Old" sibling inlines
// the previous implementation so a side-by-side allocs/op read shows the delta.
// Run: go test -run=^$ -bench=WarmPath -benchmem ./pkg/router/

// benchSink defeats dead-code elimination so a benchmarked allocation actually
// escapes (otherwise the compiler proves an unused buffer never escapes and
// reports 0 allocs/op, hiding the real cost).
var benchSink []byte

func BenchmarkWarmPathFunctionMetadataHeaderNew(b *testing.B) {
	meta := &metav1.ObjectMeta{UID: "0a1b2c3d-uid", Name: "hello", Namespace: "default", ResourceVersion: "12345"}
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	b.ReportAllocs()
	for b.Loop() {
		setFunctionMetadataToHeader(meta, req)
	}
}

func BenchmarkWarmPathFunctionMetadataHeaderOld(b *testing.B) {
	meta := &metav1.ObjectMeta{UID: "0a1b2c3d-uid", Name: "hello", Namespace: "default", ResourceVersion: "12345"}
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	b.ReportAllocs()
	for b.Loop() {
		// Previous implementation: rebuilt the four constant header names with
		// fmt.Sprintf on every request.
		req.Header.Set(fmt.Sprintf("X-%s-Uid", HEADERS_FISSION_FUNCTION_PREFIX), string(meta.UID))
		req.Header.Set(fmt.Sprintf("X-%s-Name", HEADERS_FISSION_FUNCTION_PREFIX), meta.Name)
		req.Header.Set(fmt.Sprintf("X-%s-Namespace", HEADERS_FISSION_FUNCTION_PREFIX), meta.Namespace)
		req.Header.Set(fmt.Sprintf("X-%s-ResourceVersion", HEADERS_FISSION_FUNCTION_PREFIX), meta.ResourceVersion)
	}
}

func BenchmarkWarmPathFunctionCallAttrsNew(b *testing.B) {
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		// Vary the status code a little to exercise the cache realistically.
		_ = functionCallAttrs("default", "hello", "", "/hello", http.MethodGet, 200+(i&1))
		i++
	}
}

func BenchmarkWarmPathFunctionCallAttrsOld(b *testing.B) {
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		// Previous implementation: built and sorted a fresh 5-attribute Set per
		// request (plus fmt.Sprint of the status code).
		_ = metric.WithAttributes(
			attribute.String("function_namespace", "default"),
			attribute.String("function_name", "hello"),
			attribute.String("path", "/hello"),
			attribute.String("method", http.MethodGet),
			attribute.String("code", strconv.Itoa(200+(i&1))),
		)
		i++
	}
}

func BenchmarkWarmPathProxyBufferNew(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		buf := proxyResponseBufferPool.Get()
		buf[0] = 1 // touch so it escapes, mirroring a real copy
		benchSink = buf
		proxyResponseBufferPool.Put(buf)
	}
}

func BenchmarkWarmPathProxyBufferOld(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		// Previous behavior: httputil.ReverseProxy allocates a 32 KiB copy buffer
		// per response when BufferPool is nil.
		buf := make([]byte, 32*1024)
		buf[0] = 1
		benchSink = buf
	}
}

// When no OTLP exporter is configured the span is non-recording (NeverSample),
// so guarding the event emission with SpanIsRecording skips building the
// attribute map + slice entirely. ctx with no span is non-recording.
func BenchmarkWarmPathTrackEventGuardedNew(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if otelUtils.SpanIsRecording(ctx) {
			otelUtils.SpanTrackEvent(ctx, "roundtrip", otelUtils.MapToAttributes(map[string]string{
				"function-name": "hello", "function-namespace": "default",
			})...)
		}
	}
}

func BenchmarkWarmPathTrackEventUnguardedOld(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		// Previous behavior: build the map + slice every request, then AddEvent
		// no-ops on the non-recording span — the construction is wasted.
		otelUtils.SpanTrackEvent(ctx, "roundtrip", otelUtils.MapToAttributes(map[string]string{
			"function-name": "hello", "function-namespace": "default",
		})...)
	}
}

func BenchmarkWarmPathForwardedHostHeaderNew(b *testing.B) {
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	req.Host = "example.com:8888"
	b.ReportAllocs()
	for b.Loop() {
		req.Header.Del(FORWARDED)
		req.Header.Del(X_FORWARDED_HOST)
		addForwardedHostHeader(req)
	}
}

func BenchmarkWarmPathForwardedHostHeaderOld(b *testing.B) {
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	req.Host = "example.com:8888"
	b.ReportAllocs()
	for b.Loop() {
		req.Header.Del(FORWARDED)
		req.Header.Del(X_FORWARDED_HOST)
		// Previous implementation: built a "<proto>://<host>" pseudo-URL and
		// url.Parse'd it per request just to extract the hostname (which the
		// bogus scheme made empty anyway — see addForwardedHostHeader).
		reqURL := fmt.Sprintf("%s://%s", req.Proto, req.Host)
		u, err := url.Parse(reqURL)
		if err != nil {
			b.Fatal(err)
		}
		var host string
		ip := net.ParseIP(u.Hostname())
		if ip == nil || ip.To4() != nil {
			host = fmt.Sprintf(`host=%s;`, req.Host)
		} else if ip.To16() != nil {
			host = fmt.Sprintf(`host="%s";`, req.Host)
		}
		req.Header.Set(FORWARDED, host)
		req.Header.Set(X_FORWARDED_HOST, req.Host)
	}
}

func BenchmarkWarmPathParamsHeaderNew(b *testing.B) {
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	b.ReportAllocs()
	for b.Loop() {
		req.Header.Set("X-Fission-Params-"+"name", "value")
	}
}

func BenchmarkWarmPathParamsHeaderOld(b *testing.B) {
	req := httptest.NewRequest(http.MethodGet, "/hello", nil)
	b.ReportAllocs()
	for b.Loop() {
		req.Header.Set(fmt.Sprintf("X-Fission-Params-%v", "name"), "value")
	}
}
