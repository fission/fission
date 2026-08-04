// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateInternalAuthEnvIsWiredIntoEveryBinary pins the load-bearing half
// of the fail-closed contract that TestValidateInternalAuthEnv cannot reach:
// the function is only a guarantee if it is actually CALLED at each binary's
// entry point before any verifier is built. Deleting that call from a main is
// otherwise invisible — the unit test over the function stays green. This
// asserts the call site is present in every shipped entry point by reading the
// source, the same source-assertion style cmd/fission-bundle/main_test.go uses
// for its dispatch table.
func TestValidateInternalAuthEnvIsWiredIntoEveryBinary(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	// this file lives at pkg/apis/core/v1/ — four levels below the repo root.
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")

	entryPoints := []string{
		"cmd/fission-bundle/main.go",
		"cmd/fetcher/app/server.go",
		"cmd/builder/app/server.go",
		"cmd/fission-cli/main.go",
	}
	for _, rel := range entryPoints {
		src, err := os.ReadFile(filepath.Join(repoRoot, rel))
		require.NoErrorf(t, err, "reading entry point %s", rel)
		assert.Truef(t, strings.Contains(string(src), "ValidateInternalAuthEnv()"),
			"%s must call fv1.ValidateInternalAuthEnv() at startup — the fail-closed guarantee is only as good as its call sites", rel)
	}
}
