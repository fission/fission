// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestBindAddrFromEnv pins the shared bind-address resolution every component
// uses for its metrics/health servers (formerly per-package bindAddr copies).
func TestBindAddrFromEnv(t *testing.T) {
	t.Setenv("METRICS_ADDR", "")
	assert.Equal(t, ":8080", BindAddrFromEnv("METRICS_ADDR", 8080))

	t.Setenv("METRICS_ADDR", "9090")
	assert.Equal(t, ":9090", BindAddrFromEnv("METRICS_ADDR", 8080))

	t.Setenv("METRICS_ADDR", "0.0.0.0:9090")
	assert.Equal(t, "0.0.0.0:9090", BindAddrFromEnv("METRICS_ADDR", 8080))

	t.Setenv("METRICS_ADDR", "0")
	assert.Equal(t, ":0", BindAddrFromEnv("METRICS_ADDR", 8080))
}
