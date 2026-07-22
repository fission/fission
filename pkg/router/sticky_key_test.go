// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func stickyFn(source fv1.StickySource, name string) *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default"},
		Spec: fv1.FunctionSpec{
			State: &fv1.StateConfig{Sticky: &fv1.StickyConfig{Source: source, Name: name}},
		},
	}
}

func TestStickyKeyFromRequest(t *testing.T) {
	t.Parallel()

	withHeader := httptest.NewRequest("GET", "/fn", nil)
	withHeader.Header.Set("X-Session-Id", "abc")
	withQuery := httptest.NewRequest("GET", "/fn?session=xyz&other=1", nil)
	bare := httptest.NewRequest("GET", "/fn", nil)

	t.Run("header source", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "abc", stickyKeyFromRequest(stickyFn(fv1.StickySourceHeader, "X-Session-Id"), withHeader))
		assert.Empty(t, stickyKeyFromRequest(stickyFn(fv1.StickySourceHeader, "X-Session-Id"), bare), "declared but missing => default pick")
	})

	t.Run("query source", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "xyz", stickyKeyFromRequest(stickyFn(fv1.StickySourceQueryParam, "session"), withQuery))
		assert.Empty(t, stickyKeyFromRequest(stickyFn(fv1.StickySourceQueryParam, "missing"), withQuery))
	})

	t.Run("not sticky-declared", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, stickyKeyFromRequest(nil, withHeader))
		assert.Empty(t, stickyKeyFromRequest(&fv1.Function{}, withHeader))
		noSticky := &fv1.Function{Spec: fv1.FunctionSpec{State: &fv1.StateConfig{}}}
		assert.Empty(t, stickyKeyFromRequest(noSticky, withHeader))
	})
}
