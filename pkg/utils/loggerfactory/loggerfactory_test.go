// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package loggerfactory

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetLoggerMemoized asserts that GetLogger() returns a logger backed by the
// same underlying sink across calls, proving the base zap core/sampler is
// constructed once and shared rather than re-allocated per call.
func TestGetLoggerMemoized(t *testing.T) {
	a := GetLogger()
	b := GetLogger()

	sinkA := a.GetSink()
	sinkB := b.GetSink()
	require.NotNil(t, sinkA)
	require.NotNil(t, sinkB)

	assert.Equal(t,
		reflect.ValueOf(sinkA).Pointer(),
		reflect.ValueOf(sinkB).Pointer(),
		"GetLogger() should return a logger backed by the same memoized sink",
	)
}

// TestGetLoggerLogsWithoutPanic is a basic smoke test ensuring the memoized
// logger is usable.
func TestGetLoggerLogsWithoutPanic(t *testing.T) {
	logger := GetLogger().WithName("smoke").WithValues("k", "v")
	assert.NotPanics(t, func() {
		logger.Info("smoke test message", "field", "value")
		logger.V(1).Info("debug-ish message")
	})
}
