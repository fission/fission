// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0
package helm

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fission/fission/pkg/svcinfo"
)

// TestAsyncInvocationChart checks the RFC-0024 chart wiring: the router gains the
// async env and the internal-listener NetworkPolicy admits svc: router (the
// dispatcher's cross-replica delivery) only when asyncInvocation.enabled, and
// neither renders by default.
func TestAsyncInvocationChart(t *testing.T) {
	t.Run("enabled: router env + svc:router NetworkPolicy row", func(t *testing.T) {
		docs := render(t,
			"--set", "asyncInvocation.enabled=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
			"--set", "networkPolicy.enabled=true")

		env := containerEnv(t, deployment(t, docs, svcinfo.SvcRouter))
		assert.Equal(t, "true", env["ASYNC_INVOCATION_ENABLED"])
		assert.Equal(t, "client", env["STATESTORE_DRIVER"])
		assert.Contains(t, env["STATESTORE_DSN"], svcinfo.SvcStatestore)
		assert.Equal(t, svcinfo.RouterInternalURL("fission"), env["ROUTER_INTERNAL_URL"])

		np := networkPolicy(t, docs, "router-allow-ingress")
		assert.True(t, npAllowsFromSvc(np, svcinfo.SvcRouter),
			"async delivery is cross-replica; the internal-listener allowlist must admit svc: router")
	})

	t.Run("disabled by default: no async env, no svc:router row", func(t *testing.T) {
		docs := render(t, "--set", "networkPolicy.enabled=true")
		env := containerEnv(t, deployment(t, docs, svcinfo.SvcRouter))
		assert.NotContains(t, env, "ASYNC_INVOCATION_ENABLED")
		np := networkPolicy(t, docs, "router-allow-ingress")
		assert.False(t, npAllowsFromSvc(np, svcinfo.SvcRouter),
			"svc: router must not be in the allowlist unless async is enabled")
	})
}
