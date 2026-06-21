// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/fission-cli/cmd"
)

// TestRunLocalIsClusterOptional guards the contract that `function run-local`
// can run without a kubeconfig (the --image cluster-less path): the root
// PersistentPreRunE keys off this annotation to skip the hard client-build
// failure. Dropping it would silently re-break cluster-less runs.
func TestRunLocalIsClusterOptional(t *testing.T) {
	var runLocal *cobra.Command
	for _, c := range Commands().Commands() {
		if c.Name() == "run-local" {
			runLocal = c
			break
		}
	}
	require.NotNil(t, runLocal, "run-local subcommand should be registered")
	assert.Equal(t, "true", runLocal.Annotations[cmd.ClusterOptionalAnnotation],
		"run-local must be marked cluster-optional so --image works without a kubeconfig")
}
