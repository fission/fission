// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package throttler

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// expiryService runs for the lifetime of a Throttler with no shutdown hook.
		goleak.IgnoreTopFunction("github.com/fission/fission/pkg/throttler.(*Throttler).expiryService"),
	)
}
