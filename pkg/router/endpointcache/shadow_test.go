// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package endpointcache

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

type fakeResolver struct {
	url         *url.URL
	err         error
	invalidated int
}

func (f *fakeResolver) Resolve(context.Context, *fv1.Function) (*url.URL, bool, error) {
	return f.url, true, f.err
}
func (f *fakeResolver) Invalidate(*fv1.Function) { f.invalidated++ }

func fnOfType(name string, et fv1.ExecutorType) *fv1.Function {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType = et
	return fn
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

func shadowCount(t *testing.T, result string) float64 {
	t.Helper()
	return testutil.ToFloat64(shadowResults.WithLabelValues(result))
}

func TestShadowClassification(t *testing.T) {
	// Not parallel: asserts deltas on the package-level counter.
	ix := NewIndex()
	ix.ApplySlice(slice("s1", "fn-pool", "default", 8888, "10.0.0.1"))

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
			wantResult: resultMatch,
		},
		{
			name:       "poolmgr address not yet in slices is lag",
			fn:         fnOfType("fn-pool", fv1.ExecutorTypePoolmgr),
			answer:     "http://10.0.0.99:8888",
			wantResult: resultLag,
		},
		{
			name:       "poolmgr with no endpoints is a miss",
			fn:         fnOfType("fn-unknown", fv1.ExecutorTypePoolmgr),
			answer:     "http://10.0.0.1:8888",
			wantResult: resultMiss,
		},
		{
			name:       "newdeploy compares state not address: endpoints present is a match",
			fn:         fnOfType("fn-pool", fv1.ExecutorTypeNewdeploy),
			answer:     "http://svc-x.default:80",
			wantResult: resultMatch,
		},
		{
			name:       "newdeploy with no endpoints is a miss",
			fn:         fnOfType("fn-unknown", fv1.ExecutorTypeNewdeploy),
			answer:     "http://svc-x.default:80",
			wantResult: resultMiss,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &fakeResolver{url: mustURL(t, tt.answer)}
			s := NewShadow(logr.Discard(), inner, ix)

			before := shadowCount(t, tt.wantResult)
			u, fromCache, err := s.Resolve(t.Context(), tt.fn)
			require.NoError(t, err)
			assert.True(t, fromCache, "shadow must pass the inner answer through untouched")
			assert.Equal(t, tt.answer, u.String())
			assert.Equal(t, before+1, shadowCount(t, tt.wantResult))
		})
	}
}

func TestShadowNeverComparesOnError(t *testing.T) {
	ix := NewIndex()
	inner := &fakeResolver{err: errors.New("boom")}
	s := NewShadow(logr.Discard(), inner, ix)

	before := shadowCount(t, resultMiss)
	_, _, err := s.Resolve(t.Context(), fnOfType("fn-x", fv1.ExecutorTypePoolmgr))
	require.Error(t, err)
	assert.Equal(t, before, shadowCount(t, resultMiss), "errors are not compared")
}

func TestShadowDelegatesInvalidate(t *testing.T) {
	inner := &fakeResolver{}
	s := NewShadow(logr.Discard(), inner, NewIndex())
	s.Invalidate(fnOfType("fn-x", fv1.ExecutorTypePoolmgr))
	assert.Equal(t, 1, inner.invalidated)
}

// TestShadowNeverMutatesIndex: comparisons are read-only.
func TestShadowNeverMutatesIndex(t *testing.T) {
	ix := NewIndex()
	ix.ApplySlice(slice("s1", "fn-pool", "default", 8888, "10.0.0.1"))
	s := NewShadow(logr.Discard(), &fakeResolver{url: mustURL(t, "http://10.0.0.1:8888")}, ix)

	for range 5 {
		_, _, err := s.Resolve(t.Context(), fnOfType("fn-pool", fv1.ExecutorTypePoolmgr))
		require.NoError(t, err)
	}
	assert.Equal(t, 1, ix.Size())
	assert.Len(t, ix.Lookup("default", "fn-pool"), 1)
}
