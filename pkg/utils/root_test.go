// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelUnderRoot(t *testing.T) {
	t.Parallel()
	base := filepath.Clean("/packages")

	ok := []struct{ in, want string }{
		{"file.txt", "file.txt"},
		{"sub/file.txt", filepath.Join("sub", "file.txt")},
		{"/packages/file.txt", "file.txt"},
		{"/packages/sub/file.txt", filepath.Join("sub", "file.txt")},
		{"foo/../bar", "bar"}, // resolves to an in-base path; not traversal
		{".", "."},            // the base itself is allowed
		{"/packages", "."},    // base by absolute path
	}
	for _, c := range ok {
		got, err := relUnderRoot(base, c.in)
		require.NoError(t, err, "input %q", c.in)
		assert.Equal(t, c.want, got, "input %q", c.in)
	}

	bad := []string{"../escape", "../../escape", "/etc/passwd", "sub/../../escape"}
	for _, in := range bad {
		_, err := relUnderRoot(base, in)
		assert.Error(t, err, "input %q should be rejected", in)
	}
}

func TestRootHelpersConfineToBase(t *testing.T) {
	t.Parallel()

	t.Run("write/stat/rename happy path", func(t *testing.T) {
		t.Parallel()
		base := t.TempDir()

		require.NoError(t, RootWriteFile(base, "a.txt", []byte("alpha"), 0o600))
		fi, err := RootStat(base, "a.txt")
		require.NoError(t, err)
		assert.Equal(t, int64(5), fi.Size())

		// absolute-under-base form also works (parent created first, since
		// RootWriteFile does not create parents — same as os.WriteFile)
		require.NoError(t, RootMkdirAll(base, "nested", 0o755))
		require.NoError(t, RootWriteFile(base, filepath.Join(base, "nested", "b.txt"), []byte("beta"), 0o600))
		require.NoError(t, RootMkdirAll(base, "d1/d2", 0o755))

		require.NoError(t, RootRename(base, "a.txt", "renamed.txt"))
		_, err = RootStat(base, "a.txt")
		assert.Error(t, err)
		fi, err = RootStat(base, "renamed.txt")
		require.NoError(t, err)
		assert.Equal(t, int64(5), fi.Size())
	})

	t.Run("escapes are rejected and leave the filesystem untouched", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		base := filepath.Join(root, "base")
		require.NoError(t, os.MkdirAll(base, 0o755))
		sentinel := filepath.Join(root, "sentinel")
		require.NoError(t, os.WriteFile(sentinel, []byte("intact"), 0o600))

		assert.Error(t, RootWriteFile(base, "../sentinel", []byte("pwned"), 0o600))
		assert.Error(t, RootWriteFile(base, sentinel, []byte("pwned"), 0o600))
		assert.Error(t, RootMkdirAll(base, "../evil", 0o755))
		_, err := RootStat(base, "../sentinel")
		assert.Error(t, err)
		assert.Error(t, RootRename(base, "../sentinel", "x"))

		// sentinel outside base is untouched, and no escape dir was created
		got, err := os.ReadFile(sentinel)
		require.NoError(t, err)
		assert.Equal(t, "intact", string(got))
		assert.NoDirExists(t, filepath.Join(root, "evil"))
	})

	t.Run("RootJoin returns validated absolute path", func(t *testing.T) {
		t.Parallel()
		base := filepath.Clean("/packages")
		got, err := RootJoin(base, "sub/file.txt")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(base, "sub", "file.txt"), got)
		_, err = RootJoin(base, "../escape")
		assert.Error(t, err)
	})
}
