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
