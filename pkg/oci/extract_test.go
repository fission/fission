// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tarEntry describes one entry of a synthesized image layer.
type tarEntry struct {
	typeflag byte
	mode     int64
	linkname string
	body     string
}

func file(body string) tarEntry {
	return tarEntry{typeflag: tar.TypeReg, mode: 0o644, body: body}
}

func dir() tarEntry {
	return tarEntry{typeflag: tar.TypeDir, mode: 0o755}
}

// makeLayer builds a single tar layer from the given entries, in map
// iteration-independent order (sorted by the caller's literal order is not
// needed; tar readers don't care).
func makeLayer(t *testing.T, entries map[string]tarEntry) v1.Layer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, e := range entries {
		hdr := &tar.Header{
			Name:     name,
			Typeflag: e.typeflag,
			Mode:     e.mode,
			Linkname: e.linkname,
			Size:     int64(len(e.body)),
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if e.typeflag == tar.TypeReg {
			_, err := tw.Write([]byte(e.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	raw := buf.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(raw)), nil
	})
	require.NoError(t, err)
	return layer
}

func makeImage(t *testing.T, layers ...v1.Layer) v1.Image {
	t.Helper()
	img, err := mutate.AppendLayers(empty.Image, layers...)
	require.NoError(t, err)
	return img
}

// pushImage starts an in-memory registry over plain HTTP, pushes img to it
// under repo:tag, and returns the host (host:port) plus the full reference.
func pushImage(t *testing.T, img v1.Image, repo, tag string) (host, ref string) {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	host = u.Host
	ref = fmt.Sprintf("%s/%s:%s", host, repo, tag)
	parsed, err := name.ParseReference(ref, name.Insecure)
	require.NoError(t, err)
	require.NoError(t, remote.Write(parsed, img))
	return host, ref
}

func imageDigest(t *testing.T, img v1.Image) string {
	t.Helper()
	d, err := img.Digest()
	require.NoError(t, err)
	return d.String()
}

func readExtracted(t *testing.T, destRoot, destDir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(destRoot, destDir, rel))
	require.NoError(t, err)
	return string(b)
}

func TestExtractImageLandsFiles(t *testing.T) {
	t.Parallel()
	img := makeImage(t, makeLayer(t, map[string]tarEntry{
		"hello.js":    file("module.exports = 'hi'"),
		"lib":         dir(),
		"lib/util.js": file("util"),
	}))
	host, ref := pushImage(t, img, "code/hello", "v1")

	destRoot := t.TempDir()
	err := ExtractImage(t.Context(), ref, destRoot, "pkg", ExtractOptions{
		InsecureRegistries: []string{host},
	})
	require.NoError(t, err)

	assert.Equal(t, "module.exports = 'hi'", readExtracted(t, destRoot, "pkg", "hello.js"))
	assert.Equal(t, "util", readExtracted(t, destRoot, "pkg", "lib/util.js"))

	info, err := os.Stat(filepath.Join(destRoot, "pkg", "hello.js"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestExtractImageSubPath(t *testing.T) {
	t.Parallel()
	img := makeImage(t, makeLayer(t, map[string]tarEntry{
		"app":          dir(),
		"app/hello.js": file("app code"),
		"other":        dir(),
		"other/x":      file("not wanted"),
	}))
	host, ref := pushImage(t, img, "code/sub", "v1")

	destRoot := t.TempDir()
	err := ExtractImage(t.Context(), ref, destRoot, "pkg", ExtractOptions{
		SubPath:            "app",
		InsecureRegistries: []string{host},
	})
	require.NoError(t, err)

	assert.Equal(t, "app code", readExtracted(t, destRoot, "pkg", "hello.js"))
	_, err = os.Stat(filepath.Join(destRoot, "pkg", "other"))
	assert.True(t, os.IsNotExist(err), "entries outside the sub-path must not be extracted")
	_, err = os.Stat(filepath.Join(destRoot, "pkg", "x"))
	assert.True(t, os.IsNotExist(err))
}

func TestExtractImageDigestMismatch(t *testing.T) {
	t.Parallel()
	img := makeImage(t, makeLayer(t, map[string]tarEntry{"a": file("a")}))
	host, ref := pushImage(t, img, "code/digest", "v1")

	wrong := "sha256:" + strings.Repeat("0", 64)
	destRoot := t.TempDir()
	err := ExtractImage(t.Context(), ref, destRoot, "pkg", ExtractOptions{
		Digest:             wrong,
		InsecureRegistries: []string{host},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), wrong, "error must name the expected digest")
	assert.Contains(t, err.Error(), imageDigest(t, img), "error must name the actual digest")
	assertNothingExtracted(t, destRoot, "pkg")
}

func TestExtractImageRejectsTraversal(t *testing.T) {
	t.Parallel()
	cases := map[string]map[string]tarEntry{
		"dotdot":   {"../evil": file("e")},
		"absolute": {"/abs/path": file("a")},
		"nested":   {"ok/../../evil": file("e")},
	}
	for tname, entries := range cases {
		t.Run(tname, func(t *testing.T) {
			t.Parallel()
			img := makeImage(t, makeLayer(t, entries))
			host, ref := pushImage(t, img, "code/trav-"+tname, "v1")

			destRoot := t.TempDir()
			err := ExtractImage(t.Context(), ref, destRoot, "pkg", ExtractOptions{
				InsecureRegistries: []string{host},
			})
			require.Error(t, err)
			// Nothing may land outside destRoot; the parent must hold only
			// destRoot itself afterwards.
			parent := filepath.Dir(destRoot)
			glob, gerr := filepath.Glob(filepath.Join(parent, "evil"))
			require.NoError(t, gerr)
			assert.Empty(t, glob, "traversal entry escaped the destination root")
		})
	}
}

func TestExtractImageRejectsSymlinkAndHardlink(t *testing.T) {
	t.Parallel()
	cases := map[string]tarEntry{
		"symlink":  {typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
		"hardlink": {typeflag: tar.TypeLink, linkname: "target"},
	}
	for tname, entry := range cases {
		t.Run(tname, func(t *testing.T) {
			t.Parallel()
			img := makeImage(t, makeLayer(t, map[string]tarEntry{"lnk": entry}))
			host, ref := pushImage(t, img, "code/link-"+tname, "v1")

			destRoot := t.TempDir()
			err := ExtractImage(t.Context(), ref, destRoot, "pkg", ExtractOptions{
				InsecureRegistries: []string{host},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "refusing to extract")
		})
	}
}

func TestExtractImageMaxBytes(t *testing.T) {
	t.Parallel()
	img := makeImage(t, makeLayer(t, map[string]tarEntry{
		"big": file(strings.Repeat("x", 1024)),
	}))
	host, ref := pushImage(t, img, "code/big", "v1")

	destRoot := t.TempDir()
	err := ExtractImage(t.Context(), ref, destRoot, "pkg", ExtractOptions{
		MaxBytes:           64,
		InsecureRegistries: []string{host},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

// TestParseReferenceInsecureDefaultOff pins the allowlist contract on the
// parsed reference's scheme: a non-allowlisted registry must be https. The
// network-level proof is impractical in a unit test because
// go-containerregistry (like Docker) treats localhost/127.0.0.1 registries as
// implicitly insecure-OK — which is also why the live-pull tests above work
// over plain HTTP without a TLS fixture.
func TestParseReferenceInsecureDefaultOff(t *testing.T) {
	t.Parallel()
	const ref = "registry.example.com/code/hello:v1"

	parsed, err := parseReference(ref, nil)
	require.NoError(t, err)
	assert.Equal(t, "https", parsed.Context().Scheme(),
		"non-allowlisted registry must require TLS")

	parsed, err = parseReference(ref, []string{"registry.example.com"})
	require.NoError(t, err)
	assert.Equal(t, "http", parsed.Context().Scheme(),
		"allowlisted registry may use plain HTTP")

	parsed, err = parseReference(ref, []string{"other.example.com"})
	require.NoError(t, err)
	assert.Equal(t, "https", parsed.Context().Scheme(),
		"allowlist must match the registry host exactly")
}

func TestExtractImageInsecureAllowlisted(t *testing.T) {
	t.Parallel()
	img := makeImage(t, makeLayer(t, map[string]tarEntry{"a": file("a")}))
	host, ref := pushImage(t, img, "code/insecure", "v1")

	destRoot := t.TempDir()
	err := ExtractImage(t.Context(), ref, destRoot, "pkg", ExtractOptions{
		InsecureRegistries: []string{host},
	})
	require.NoError(t, err)
	assert.Equal(t, "a", readExtracted(t, destRoot, "pkg", "a"))
}

// assertNothingExtracted asserts destDir under destRoot either does not exist
// or is empty — failed pulls must not leave partial artifacts behind.
func assertNothingExtracted(t *testing.T, destRoot, destDir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(destRoot, destDir))
	if os.IsNotExist(err) {
		return
	}
	require.NoError(t, err)
	assert.Empty(t, entries, "failed extraction left files behind")
}
