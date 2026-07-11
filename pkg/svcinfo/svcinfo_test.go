// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package svcinfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestURLHelpers pins the in-cluster URL forms: router/executor/storagesvc
// are fronted by Service port 80 (no explicit port); router-internal carries
// its port because its Service exposes PortRouterInternal directly.
func TestURLHelpers(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "http://router.fission", RouterURL("fission"))
	assert.Equal(t, "http://executor.fission", ExecutorURL("fission"))
	assert.Equal(t, "http://storagesvc.fission", StorageSvcURL("fission"))
	assert.Equal(t, "http://router-internal.fission:8889", RouterInternalURL("fission"))
}

// TestPortValues pins every port constant's numeric value: these numbers are
// mirrored by the Helm chart (Services, NetworkPolicies, probes), so changing
// one must be a loud, deliberate act — not a refactor side effect.
func TestPortValues(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 8888, PortRouter)
	assert.Equal(t, 8889, PortRouterInternal)
	assert.Equal(t, 8888, PortExecutor)
	assert.Equal(t, 8888, PortEnvRuntime)
	assert.Equal(t, 8000, PortFetcher)
	assert.Equal(t, 8001, PortBuilder)
	assert.Equal(t, 8000, PortStorageSvc)
	assert.Equal(t, 8890, PortMCP)
	assert.Equal(t, 8080, PortMetrics)
	assert.Equal(t, 8081, PortHealthProbe)
	assert.Equal(t, 9443, PortWebhook)
}
