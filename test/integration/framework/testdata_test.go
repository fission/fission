//go:build integration

package framework

import (
	"archive/zip"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestZipTestDataDirPreservesShellExecBit guards against a regression in
// which embed.FS strips file modes and ZipTestDataDir would emit a
// non-executable build.sh — the Fission builder then fails with
// "fork/exec ./build.sh: permission denied".
func TestZipTestDataDirPreservesShellExecBit(t *testing.T) {
	t.Parallel()
	zipPath := ZipTestDataDir(t, "python/sourcepkg", "perms-check.zip")

	r, err := zip.OpenReader(zipPath)
	require.NoError(t, err)
	defer r.Close()

	modes := map[string]uint32{}
	for _, f := range r.File {
		modes[f.Name] = uint32(f.Mode().Perm())
	}
	require.Contains(t, modes, "build.sh")
	require.Equal(t, uint32(0o755), modes["build.sh"], "build.sh should be 0755 so the builder can fork/exec it")
	require.Contains(t, modes, "user.py")
	require.Equal(t, uint32(0o644), modes["user.py"], "non-script files should be 0644")
}
