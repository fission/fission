// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDrainLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("positive grace returns native sleep preStop", func(t *testing.T) {
		t.Parallel()
		lc := DrainLifecycle(360)
		require.NotNil(t, lc)
		require.NotNil(t, lc.PreStop)
		require.NotNil(t, lc.PreStop.Sleep)
		assert.Nil(t, lc.PreStop.Exec)
		assert.Equal(t, int64(360), lc.PreStop.Sleep.Seconds)
	})

	t.Run("zero grace returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, DrainLifecycle(0))
	})

	t.Run("negative grace returns nil", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, DrainLifecycle(-1))
	})
}
