// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewBackOff(t *testing.T) {
	t.Parallel()
	b, err := NewBackOff(time.Second, 10*time.Second, 2.0, 5)
	require.NoError(t, err)
	assert.Equal(t, time.Second, b.GetInitialInterval())
	assert.Equal(t, 10*time.Second, b.GetMaxInterval())
	assert.Equal(t, 2.0, b.GetMultiplier())

	_, err = NewBackOff(-1, 10*time.Second, 2.0, 5)
	require.Error(t, err, "negative values must be rejected")
}

func TestNewDefaultBackOff(t *testing.T) {
	t.Parallel()
	b := NewDefaultBackOff()
	assert.Equal(t, DefaultInitialInterval, b.GetInitialInterval())
	assert.Equal(t, DefaultMaxInterval, b.GetMaxInterval())
	assert.Equal(t, DefaultMultiplier, b.GetMultiplier())
	assert.Equal(t, float64(DefaultMaxCount), b.GetMaxCount())
	assert.Equal(t, DefaultInitialInterval, b.GetCurrentBackoffDuration())
	assert.Equal(t, float64(0), b.GetCurrentCount())
}

func TestBackoffSetters(t *testing.T) {
	t.Parallel()
	b := NewDefaultBackOff()
	b.SetMaxCount(7)
	b.SetMultiplier(3)
	b.SetMaxInterval(time.Minute)
	b.SetInitialInterval(2 * time.Second)

	assert.Equal(t, float64(7), b.GetMaxCount())
	assert.Equal(t, 3.0, b.GetMultiplier())
	assert.Equal(t, time.Minute, b.GetMaxInterval())
	assert.Equal(t, 2*time.Second, b.GetInitialInterval())
}

func TestBackoffGetNextAndExists(t *testing.T) {
	t.Parallel()
	b := NewDefaultBackOff()

	first := b.GetNext()
	assert.Equal(t, float64(1), b.GetCurrentCount())
	assert.Equal(t, time.Duration(float64(DefaultInitialInterval)*DefaultMultiplier), first)

	second := b.GetNext()
	assert.Greater(t, second, first, "backoff grows on each step")
	assert.Equal(t, float64(2), b.GetCurrentCount())

	assert.True(t, b.NextExists(), "next exists well below the max interval")

	// Once current backoff * multiplier exceeds MaxInterval, no next.
	b.SetMaxInterval(time.Nanosecond)
	assert.False(t, b.NextExists())
}
