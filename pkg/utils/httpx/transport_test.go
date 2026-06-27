// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPooledTransport(t *testing.T) {
	t.Parallel()
	def := http.DefaultTransport.(*http.Transport)

	t.Run("widens the idle pool and raises the global cap to match", func(t *testing.T) {
		t.Parallel()
		tr := PooledTransport(128)
		assert.Equal(t, 128, tr.MaxIdleConnsPerHost)
		assert.GreaterOrEqual(t, tr.MaxIdleConns, 128, "global cap must not clamp the per-host pool")
	})

	t.Run("leaves the default global cap untouched when per-host is smaller", func(t *testing.T) {
		t.Parallel()
		tr := PooledTransport(16)
		assert.Equal(t, 16, tr.MaxIdleConnsPerHost)
		assert.Equal(t, def.MaxIdleConns, tr.MaxIdleConns)
	})

	t.Run("returns an independent clone preserving the default dial settings", func(t *testing.T) {
		t.Parallel()
		tr := PooledTransport(64)
		assert.NotSame(t, def, tr, "must not return the shared global transport")
		assert.True(t, tr.ForceAttemptHTTP2)
		assert.NotNil(t, tr.DialContext)
		assert.NotNil(t, tr.Proxy)
	})
}
