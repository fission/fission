// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSentinelsAreDistinct(t *testing.T) {
	t.Parallel()
	errs := []error{
		ErrVersionConflict,
		ErrNotFound,
		ErrCapabilityUnavailable,
		ErrQuotaExceeded,
		ErrInvalidReceipt,
		ErrClosed,
	}
	for i := range errs {
		require.Error(t, errs[i])
		for j := range errs {
			if i != j {
				require.NotErrorIs(t, errs[i], errs[j])
			}
		}
	}
}
