// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLocalObjectStoreRoundTrip exercises the os-based local backend end to
// end: put -> open & compare -> size -> list contains the id -> remove ->
// open returns ErrNotFound. It uses t.TempDir() so no live storage backend is
// required.
func TestLocalObjectStoreRoundTrip(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := newLocalObjectStore(root, "fission-functions")
	require.NoError(t, err)

	payload := []byte("the quick brown fox jumps over the lazy dog")

	id, err := store.put("archive-1", bytes.NewReader(payload), int64(len(payload)))
	require.NoError(t, err)
	require.NotEmpty(t, id)

	// the id is the ABSOLUTE path of the stored file, rooted under
	// <root>/<container>.
	assert.True(t, filepath.IsAbs(id), "expected absolute-path id, got %q", id)
	assert.Equal(t, filepath.Join(root, "fission-functions", "archive-1"), id)

	t.Run("open returns stored content", func(t *testing.T) {
		t.Parallel()
		r, err := store.open(id)
		require.NoError(t, err)
		defer r.Close()
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, payload, got)
	})

	t.Run("size matches payload", func(t *testing.T) {
		t.Parallel()
		size, err := store.size(id)
		require.NoError(t, err)
		assert.Equal(t, int64(len(payload)), size)
	})

	t.Run("exists reports true", func(t *testing.T) {
		t.Parallel()
		ok, err := store.exists(id)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("list contains the id", func(t *testing.T) {
		t.Parallel()
		infos, err := store.list("")
		require.NoError(t, err)
		ids := make([]string, 0, len(infos))
		for _, info := range infos {
			ids = append(ids, info.id)
			assert.False(t, info.lastMod.IsZero(), "lastMod should be populated")
		}
		assert.Contains(t, ids, id)
	})
}

// TestLocalObjectStoreRemove verifies remove deletes the object and that a
// subsequent open/size surfaces ErrNotFound, the sentinel the download/info
// handlers translate to HTTP 404.
func TestLocalObjectStoreRemove(t *testing.T) {
	t.Parallel()

	store, err := newLocalObjectStore(t.TempDir(), "fission-functions")
	require.NoError(t, err)

	payload := []byte("ephemeral")
	id, err := store.put("archive-2", bytes.NewReader(payload), int64(len(payload)))
	require.NoError(t, err)

	require.NoError(t, store.remove(id))

	_, err = store.open(id)
	assert.ErrorIs(t, err, ErrNotFound)

	_, err = store.size(id)
	assert.ErrorIs(t, err, ErrNotFound)

	ok, err := store.exists(id)
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestStorageClientLocalRoundTrip exercises the StorageClient wrapper methods
// the HTTP handlers use (getFileSize, copyFileToStream, exists,
// getItemIDsWithFilter, removeFileByID), seeding an object via the backend
// directly.
func TestStorageClientLocalRoundTrip(t *testing.T) {
	t.Parallel()

	storage := localStorage{
		storageType:   StorageTypeLocal,
		containerName: "fission-functions",
		localPath:     t.TempDir(),
	}
	client, err := MakeStorageClient(logr.Discard(), storage)
	require.NoError(t, err)

	payload := []byte("hello from storagesvc")
	id, err := client.backend.put("pkg-archive", bytes.NewReader(payload), int64(len(payload)))
	require.NoError(t, err)

	size, err := client.getFileSize(id)
	require.NoError(t, err)
	assert.Equal(t, int64(len(payload)), size)

	var buf bytes.Buffer
	require.NoError(t, client.copyFileToStream(id, &buf))
	assert.Equal(t, payload, buf.Bytes())

	require.NoError(t, client.exists(id))

	ids, err := client.getItemIDsWithFilter(client.filterAllItems, false)
	require.NoError(t, err)
	assert.Contains(t, ids, id)

	require.NoError(t, client.removeFileByID(id))

	assert.ErrorIs(t, client.copyFileToStream(id, &buf), ErrNotFound)
	_, err = client.getFileSize(id)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.ErrorIs(t, client.exists(id), ErrNotFound)
}

// TestLocalObjectStoreRejectsPathTraversal verifies that ids escaping the
// container directory (absolute paths outside it, or "../" traversal) are
// rejected as ErrNotFound and never touch files outside the store. The id
// comes from the request (?id=<id>), so this guards the download/info/delete
// handlers against path traversal.
func TestLocalObjectStoreRejectsPathTraversal(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// A sentinel file living OUTSIDE the container dir; it must remain
	// untouched no matter what id an attacker supplies.
	secret := filepath.Join(root, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("top secret"), 0o600))

	store, err := newLocalObjectStore(filepath.Join(root, "store"), "fission-functions")
	require.NoError(t, err)

	traversals := []string{
		secret,        // absolute path outside the container
		"/etc/passwd", // absolute path well outside
		"../secret.txt",
		"../../secret.txt", // resolves to the sentinel file
		"subdir/../../secret.txt",
	}
	for _, id := range traversals {
		t.Run(id, func(t *testing.T) {
			_, err := store.open(id)
			assert.ErrorIs(t, err, ErrNotFound)
			_, err = store.size(id)
			assert.ErrorIs(t, err, ErrNotFound)
			assert.ErrorIs(t, store.remove(id), ErrNotFound)
			ok, err := store.exists(id)
			require.NoError(t, err)
			assert.False(t, ok)
		})
	}

	// The sentinel file outside the store must still exist and be intact.
	got, err := os.ReadFile(secret)
	require.NoError(t, err)
	assert.Equal(t, []byte("top secret"), got)
}
