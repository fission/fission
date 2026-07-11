// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package svcinfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestEnvResolverPrecedence pins the resolution order: non-empty flag >
// service env override (ROUTER_INTERNAL_URL only) > POD_NAMESPACE-derived
// default > historic "fission" namespace.
func TestEnvResolverPrecedence(t *testing.T) {
	t.Run("explicit flags win", func(t *testing.T) {
		t.Setenv("POD_NAMESPACE", "prod")
		r := NewEnvResolver(FlagValues{
			ExecutorURL:   "http://exec.custom",
			RouterURL:     "http://router.custom",
			StorageSvcURL: "http://storage.custom",
		})
		assert.Equal(t, "http://exec.custom", r.ExecutorURL())
		assert.Equal(t, "http://router.custom", r.RouterURL())
		assert.Equal(t, "http://storage.custom", r.StorageSvcURL())
	})

	t.Run("unset flags derive from POD_NAMESPACE", func(t *testing.T) {
		t.Setenv("POD_NAMESPACE", "prod")
		r := NewEnvResolver(FlagValues{})
		assert.Equal(t, "http://executor.prod", r.ExecutorURL())
		assert.Equal(t, "http://router.prod", r.RouterURL())
		assert.Equal(t, "http://storagesvc.prod", r.StorageSvcURL())
	})

	t.Run("no POD_NAMESPACE falls back to fission", func(t *testing.T) {
		t.Setenv("POD_NAMESPACE", "")
		r := NewEnvResolver(FlagValues{})
		assert.Equal(t, "http://executor.fission", r.ExecutorURL())
	})

	// The established contract from cmd/fission-bundle: ROUTER_INTERNAL_URL
	// beats --routerUrl for the internal publishers' target.
	t.Run("ROUTER_INTERNAL_URL beats the routerUrl flag", func(t *testing.T) {
		t.Setenv("ROUTER_INTERNAL_URL", "http://router-internal.prod:8889")
		r := NewEnvResolver(FlagValues{RouterURL: "http://router.flagged"})
		assert.Equal(t, "http://router-internal.prod:8889", r.RouterInternalURL())
	})

	t.Run("publishers fall back to the router URL without the env", func(t *testing.T) {
		t.Setenv("ROUTER_INTERNAL_URL", "")
		r := NewEnvResolver(FlagValues{RouterURL: "http://router.flagged"})
		assert.Equal(t, "http://router.flagged", r.RouterInternalURL())
	})
}
