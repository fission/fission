// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		_ = functionCallAttrs("default", "hello", "/hello", http.MethodGet, 200+(i&1))
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
