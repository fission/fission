// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/endpointcache"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

type staticResolver struct {
	url         *url.URL
	err         error
	invalidated int
}

func (f *staticResolver) Resolve(context.Context, *fv1.Function) (ResolvedEntry, error) {
	return ResolvedEntry{SvcURL: f.url, FromCache: true}, f.err
}
func (f *staticResolver) Invalidate(*fv1.Function, *url.URL) { f.invalidated++ }

func fnOfType(name string, et fv1.ExecutorType) *fv1.Function {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = et
	return fn
}

func shadowCount(t *testing.T, result string) float64 {
	t.Helper()
	return testutil.ToFloat64(endpointcache.ShadowResultCounter(result))
}

func TestShadowClassification(t *testing.T) {
	// Not parallel: asserts deltas on the package-level counter.
	ix := endpointcache.NewIndex()
	ix.ApplySlice(fnSlice("s1", "fn-pool", "default", "10.0.0.1"))

	tests := []struct {
		name       string
		fn         *fv1.Function
		answer     string
		wantResult string
	}{
		{
			name:       "poolmgr address among ready endpoints is a match",
			fn:         fnOfType("fn-pool", fv1.ExecutorTypePoolmgr),
			answer:     "http://10.0.0.1:8888",
			wantResult: endpointcache.ShadowMatch,
		},
		{
			name:       "poolmgr address not yet in slices is lag",
			fn:         fnOfType("fn-pool", fv1.ExecutorTypePoolmgr),
			answer:     "http://10.0.0.99:8888",
			wantResult: endpointcache.ShadowLag,
		},
		{
			name:       "poolmgr with no endpoints is a miss",
			fn:         fnOfType("fn-unknown", fv1.ExecutorTypePoolmgr),
			answer:     "http://10.0.0.1:8888",
			wantResult: endpointcache.ShadowMiss,
		},
		{
			name:       "newdeploy compares state not address: endpoints present is a match",
			fn:         fnOfType("fn-pool", fv1.ExecutorTypeNewdeploy),
			answer:     "http://svc-x.default:80",
			wantResult: endpointcache.ShadowMatch,
		},
		{
			name:       "newdeploy with no endpoints is a miss",
			fn:         fnOfType("fn-unknown", fv1.ExecutorTypeNewdeploy),
			answer:     "http://svc-x.default:80",
			wantResult: endpointcache.ShadowMiss,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &staticResolver{url: mustParseURL(t, tt.answer)}
			s := newShadowResolver(loggerfactory.GetLogger(), inner, ix)

			before := shadowCount(t, tt.wantResult)
			entry, err := s.Resolve(t.Context(), tt.fn)
			require.NoError(t, err)
			assert.True(t, entry.FromCache, "shadow must pass the inner answer through untouched")
			assert.Equal(t, tt.answer, entry.SvcURL.String())
			assert.Equal(t, before+1, shadowCount(t, tt.wantResult))
		})
	}
}

func TestShadowNeverComparesOnError(t *testing.T) {
	ix := endpointcache.NewIndex()
	inner := &staticResolver{err: errors.New("boom")}
	s := newShadowResolver(loggerfactory.GetLogger(), inner, ix)

	before := shadowCount(t, endpointcache.ShadowMiss)
	_, err := s.Resolve(t.Context(), fnOfType("fn-x", fv1.ExecutorTypePoolmgr))
	require.Error(t, err)
	assert.Equal(t, before, shadowCount(t, endpointcache.ShadowMiss), "errors are not compared")
}

func TestShadowDelegatesInvalidate(t *testing.T) {
	inner := &staticResolver{}
	s := newShadowResolver(loggerfactory.GetLogger(), inner, endpointcache.NewIndex())
	s.Invalidate(fnOfType("fn-x", fv1.ExecutorTypePoolmgr), nil)
	assert.Equal(t, 1, inner.invalidated)
}

// TestShadowNeverMutatesIndex: comparisons are read-only.
func TestShadowNeverMutatesIndex(t *testing.T) {
	ix := endpointcache.NewIndex()
	ix.ApplySlice(fnSlice("s1", "fn-pool2", "default", "10.0.0.1"))
	s := newShadowResolver(loggerfactory.GetLogger(), &staticResolver{url: mustParseURL(t, "http://10.0.0.1:8888")}, ix)

	for range 5 {
		_, err := s.Resolve(t.Context(), fnOfType("fn-pool2", fv1.ExecutorTypePoolmgr))
		require.NoError(t, err)
	}
	assert.Len(t, ix.Lookup("default", "fn-pool2"), 1)
}
