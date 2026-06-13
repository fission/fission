// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"io"
	"log"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/go-containerregistry/pkg/registry"
)

// newTestRegistry starts an in-memory OCI registry and returns its host.
func newTestRegistry(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return u.Host
}

// writePackageDir builds a representative deployment directory.
func writePackageDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "lib"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.py"), []byte("def main(): return 'hi'\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lib", "util.py"), []byte("x = 1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "exec.sh"), []byte("#!/bin/sh\n"), 0o755))
	return dir
}

// TestPushDirectoryRoundTrip pins the producer's artifact shape: a single
// OCI-media-type layer whose content equals the directory, digest-pinned,
// digest-tagged, and — the load-bearing property — consumable by BOTH
// consumption paths: ExtractImage (Path A) reproduces the directory.
func TestPushDirectoryRoundTrip(t *testing.T) {
	host := newTestRegistry(t)
	dir := writePackageDir(t)
	repo := host + "/ns/pkg"

	imageRef, digest, err := PushDirectory(t.Context(), dir, repo, PushOptions{InsecureRegistries: []string{host}})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(digest, "sha256:"), "digest form")
	assert.Contains(t, imageRef, repo+":", "tagged reference")
	assert.Contains(t, imageRef, digest[len("sha256:"):len("sha256:")+12], "tag is the short digest")

	// Manifest shape: OCI image manifest, exactly one layer, OCI layer media
	// type (kubelet image volumes and containerd consume image manifests —
	// deliberately not an ORAS artifact).
	ref, err := name.ParseReference(imageRef, name.Insecure)
	require.NoError(t, err)
	img, err := remote.Image(ref)
	require.NoError(t, err)
	mt, err := img.MediaType()
	require.NoError(t, err)
	assert.Equal(t, types.OCIManifestSchema1, mt)
	layers, err := img.Layers()
	require.NoError(t, err)
	require.Len(t, layers, 1, "single-layer artifact")
	lmt, err := layers[0].MediaType()
	require.NoError(t, err)
	assert.Equal(t, types.OCILayer, lmt)
	gotDigest, err := img.Digest()
	require.NoError(t, err)
	assert.Equal(t, digest, gotDigest.String(), "returned digest must be the pushed image's digest")

	// Path A consumption: ExtractImage (digest-pinned) reproduces the dir.
	dest := t.TempDir()
	err = ExtractImage(t.Context(), imageRef, dest, "out", ExtractOptions{
		Digest:             digest,
		InsecureRegistries: []string{host},
	})
	require.NoError(t, err)
	for rel, want := range map[string]string{
		"main.py":     "def main(): return 'hi'\n",
		"lib/util.py": "x = 1\n",
		"exec.sh":     "#!/bin/sh\n",
	} {
		got, err := os.ReadFile(filepath.Join(dest, "out", rel))
		require.NoErrorf(t, err, "extracted file %s", rel)
		assert.Equal(t, want, string(got))
	}
	// Executable bit survives the round trip.
	st, err := os.Stat(filepath.Join(dest, "out", "exec.sh"))
	require.NoError(t, err)
	assert.NotZero(t, st.Mode()&0o100, "executable bit must survive")
}

// TestPushDirectoryDeterministic pins reproducibility: identical content
// pushes to an identical digest (registry blob dedup; rebuilds are no-ops).
func TestPushDirectoryDeterministic(t *testing.T) {
	host := newTestRegistry(t)
	dir := writePackageDir(t)

	_, d1, err := PushDirectory(t.Context(), dir, host+"/ns/a", PushOptions{InsecureRegistries: []string{host}})
	require.NoError(t, err)
	_, d2, err := PushDirectory(t.Context(), dir, host+"/ns/b", PushOptions{InsecureRegistries: []string{host}})
	require.NoError(t, err)
	assert.Equal(t, d1, d2, "identical content must produce an identical digest")

	// Content change moves the digest.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.py"), []byte("changed\n"), 0o644))
	_, d3, err := PushDirectory(t.Context(), dir, host+"/ns/a", PushOptions{InsecureRegistries: []string{host}})
	require.NoError(t, err)
	assert.NotEqual(t, d1, d3)
}

// TestPushDirectoryRejectsSymlinks pins producer/consumer consistency: the
// Path A extractor refuses links, so the producer must refuse to publish them.
func TestPushDirectoryRejectsSymlinks(t *testing.T) {
	host := newTestRegistry(t)
	dir := writePackageDir(t)
	require.NoError(t, os.Symlink("main.py", filepath.Join(dir, "alias.py")))

	_, _, err := PushDirectory(t.Context(), dir, host+"/ns/pkg", PushOptions{InsecureRegistries: []string{host}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to publish")
}

// TestPushDirectoryRegistryDown pins the failure mode the buildermgr
// fallback consumes: an unreachable registry is a clean error, not a hang.
func TestPushDirectoryRegistryDown(t *testing.T) {
	dir := writePackageDir(t)
	// Reserved-but-unroutable host with an insecure allowlist entry so the
	// failure is the connection, not TLS.
	_, _, err := PushDirectory(t.Context(), dir, "127.0.0.1:1/ns/pkg", PushOptions{InsecureRegistries: []string{"127.0.0.1:1"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pushing")
}
