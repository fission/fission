// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestSupportDump exercises `fission support dump`, which collects cluster +
// fission component specs and logs into a zip archive. It drives a large,
// otherwise-uncovered slice of pkg/fission-cli/cmd/support (dump.go and the
// resources/* dumpers) end to end against the real cluster. The dumpers are
// best-effort, so a clean exit plus a produced archive is the assertion.
func TestSupportDump(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)

	outDir := filepath.Join(t.TempDir(), "dump")
	out := ns.CLICaptureStdout(t, ctx, "support", "dump", "--output", outDir)

	matches, err := filepath.Glob(filepath.Join(outDir, "fission-dump_*.zip"))
	require.NoError(t, err)
	require.NotEmpty(t, matches, "support dump should produce a zip archive; output:\n%s", out)
}
