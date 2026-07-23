// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

// Mux match + incremental-churn benchmarks (RFC-0013).
//
// BenchmarkMuxMatch pins httpmux's per-request linear route scan at 10k routes
// (first/last/miss positions) — the native matcher built in RFC-0013 phase 3.
// BenchmarkIncrementalWeightTick measures the steady-churn unit: one canary
// weight tick applied through the incremental path (an O(1) handler swap, not
// an O(full rebuild)).

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/router/routetable"
	"github.com/fission/fission/pkg/utils/httpmux"
)

// benchRouteSet builds N functions and N triggers (trigger bench-i routes
// /bench-i to function bench-fn-i) plus the function-timeout map.
func benchRouteSet(n int) ([]fv1.Function, []fv1.HTTPTrigger, map[crd.CacheKeyUG]int) {
	fns := make([]fv1.Function, 0, n)
	triggers := make([]fv1.HTTPTrigger, 0, n)
	fnTimeout := make(map[crd.CacheKeyUG]int, n)
	for i := range n {
		fn := fv1.Function{ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("bench-fn-%d", i),
			Namespace: "default",
			UID:       types.UID(fmt.Sprintf("uid-%d", i)),
		}}
		fns = append(fns, fn)
		fnTimeout[crd.CacheKeyUGFromMeta(&fn.ObjectMeta)] = fv1.DEFAULT_FUNCTION_TIMEOUT
		triggers = append(triggers, fv1.HTTPTrigger{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("bench-%d", i), Namespace: "default"},
			Spec: fv1.HTTPTriggerSpec{
				FunctionReference: fv1.FunctionReference{
					Type: fv1.FunctionReferenceTypeFunctionName,
					Name: fn.Name,
				},
				RelativeURL: fmt.Sprintf("/bench-%d", i),
				Methods:     []string{http.MethodGet},
			},
		})
	}
	return fns, triggers, fnTimeout
}

// nopResponseWriter discards everything: a reusable sink so the match loop
// measures dispatch (scan + the trivial matched/404 handler), not per-iteration
// httptest.NewRecorder allocations.
type nopResponseWriter struct{ h http.Header }

func (w *nopResponseWriter) Header() http.Header {
	if w.h == nil {
		w.h = http.Header{}
	}
	return w.h
}
func (w *nopResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *nopResponseWriter) WriteHeader(int)             {}

func BenchmarkMuxMatch(b *testing.B) {
	const n = 10000
	// Register the same 10k exact routes buildMuxes would, with no-op handlers
	// so the loop measures the dispatcher's scan, not proxy work. Handler()
	// compiles once (static paths compile to no regexp), exactly as production.
	m := httpmux.New()
	ok := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for i := range n {
		m.HandleFunc(fmt.Sprintf("/bench-%d", i), ok).Methods(http.MethodGet)
	}
	h := m.Handler()

	cases := []struct {
		name string
		path string
	}{
		// httpmux matches in registration order: the first route is the best
		// case, the last is the worst case, and a miss walks the entire route
		// list before falling through to 404.
		{name: "first-route", path: "/bench-0"},
		{name: "last-route", path: fmt.Sprintf("/bench-%d", n-1)},
		{name: "miss", path: "/no-such-route"},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			w := &nopResponseWriter{}
			b.ReportAllocs()
			for b.Loop() {
				h.ServeHTTP(w, req)
			}
		})
	}
}

// BenchmarkIncrementalWeightTick measures the RFC-0013 steady-churn unit: one
// canary weight tick applied through the incremental path while the table
// holds 10k other routes. It asserts the tick is an O(1) handler swap rather
// than an O(full rebuild) of the whole mux.
func BenchmarkIncrementalWeightTick(b *testing.B) {
	const n = 10000
	fns, triggers, _ := benchRouteSet(n)
	canary := fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "bench-canary", Namespace: "default", Generation: 1, UID: "uid-canary"},
		Spec: fv1.HTTPTriggerSpec{
			RelativeURL: "/bench-canary",
			Methods:     []string{http.MethodGet},
			FunctionReference: fv1.FunctionReference{
				Type:            fv1.FunctionReferenceTypeFunctionWeights,
				FunctionWeights: map[string]int{"bench-fn-0": 90, "bench-fn-1": 10},
			},
		},
	}
	triggers = append(triggers, canary)

	objs := make([]client.Object, 0, len(fns)+len(triggers))
	for i := range fns {
		objs = append(objs, &fns[i])
	}
	for i := range triggers {
		objs = append(objs, &triggers[i])
	}
	ts, _ := newIncrementalTS(b, objs...)
	if _, err := ts.resync(b.Context(), true); err != nil {
		b.Fatal(err)
	}
	ts.materialize(b.Context())

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		i++
		tick := canary.DeepCopy()
		tick.Generation = int64(i + 1)
		tick.Spec.FunctionReference.FunctionWeights = map[string]int{
			"bench-fn-0": 90 - (i % 80), "bench-fn-1": 10 + (i % 80),
		}
		res, err := ts.applyTriggerIncremental(b.Context(), tick)
		if err != nil {
			b.Fatal(err)
		}
		if res != routetable.HandlerSwapped {
			b.Fatalf("expected a handler swap, got %v", res)
		}
	}
}
