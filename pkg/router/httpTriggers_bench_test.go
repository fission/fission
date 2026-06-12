// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

// Mux build + match benchmarks (RFC-0013 phase 0 baselines).
//
// BenchmarkBuildMuxes measures the cost of one full mux rebuild at N
// triggers + N functions — which today is also the cost of a single canary
// weight tick, since any trigger or function event rebuilds everything.
// BenchmarkMuxMatch pins gorilla's per-request linear route scan at 10k
// routes (first/last/miss positions) — the evidence input for the RFC's
// phase-3 gate (build the native matcher only if p99 match overhead > 1ms).

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// benchRouteSet builds N functions and N triggers (trigger bench-i routes
// /bench-i to function bench-fn-i) plus the function-timeout map updateRouter
// would pass to buildMuxes.
func benchRouteSet(n int) ([]fv1.Function, []fv1.HTTPTrigger, map[types.UID]int) {
	fns := make([]fv1.Function, 0, n)
	triggers := make([]fv1.HTTPTrigger, 0, n)
	fnTimeout := make(map[types.UID]int, n)
	for i := range n {
		fn := fv1.Function{ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("bench-fn-%d", i),
			Namespace: "default",
			UID:       types.UID(fmt.Sprintf("uid-%d", i)),
		}}
		fns = append(fns, fn)
		fnTimeout[fn.UID] = fv1.DEFAULT_FUNCTION_TIMEOUT
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

func BenchmarkBuildMuxes(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("triggers=%d", n), func(b *testing.B) {
			fns, triggers, fnTimeout := benchRouteSet(n)
			ts := newShapeTS(b, fns, triggers)
			ctx := b.Context()
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := ts.buildMuxes(ctx, fnTimeout); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkMuxMatch(b *testing.B) {
	const n = 10000
	fns, triggers, fnTimeout := benchRouteSet(n)
	ts := newShapeTS(b, fns, triggers)
	public, _, err := ts.buildMuxes(b.Context(), fnTimeout)
	if err != nil {
		b.Fatal(err)
	}

	cases := []struct {
		name string
		path string
	}{
		// gorilla matches in registration order: the first route is the
		// best case, the last is the worst case, and a miss walks the
		// entire route list before falling through to 404.
		{name: "first-route", path: "/bench-0"},
		{name: "last-route", path: fmt.Sprintf("/bench-%d", n-1)},
		{name: "miss", path: "/no-such-route"},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			b.ReportAllocs()
			for b.Loop() {
				var match mux.RouteMatch
				public.Match(req, &match)
			}
		})
	}
}
