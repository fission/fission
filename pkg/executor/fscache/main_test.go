// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fscache

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// FunctionServiceCache still runs a lifetime service() loop with no
		// shutdown hook (tracked as a Phase 2 backlog item).
		goleak.IgnoreTopFunction("github.com/fission/fission/pkg/executor/fscache.(*FunctionServiceCache).service"),
	)
}
