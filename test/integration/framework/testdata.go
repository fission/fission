//go:build integration

package framework

import (
	"archive/zip"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/testdata"
)

// WriteTestData materializes an embedded testdata file under t.TempDir() and
// returns the on-disk path. The CLI then reads from that path the same way
// the bash tests read from `examples/`.
//
// Example:
//
//	codePath := framework.WriteTestData(t, "nodejs/hello/hello.js")
//	ns.CreateFunction(t, ctx, framework.FunctionOptions{Code: codePath, ...})
func WriteTestData(t *testing.T, embedPath string) string {
	t.Helper()
	b, err := testdata.FS.ReadFile(embedPath)
	require.NoErrorf(t, err, "WriteTestData: read embedded %q", embedPath)
	dir := t.TempDir()
	dst := filepath.Join(dir, filepath.Base(embedPath))
	require.NoErrorf(t, os.WriteFile(dst, b, 0o644), "WriteTestData: write %q", dst)
	return dst
}

// ZipTestDataDir packs an embedded testdata directory into a flat zip archive
// (no subdirectory entries — matches the bash idiom `zip -jr out.zip dir/`)
// under t.TempDir() and returns the archive path. Use this for source-package
// fixtures the Fission builder consumes.
//
// File modes in the resulting archive: `*.sh` files are `0755` (so the
// builder's `fork/exec ./build.sh` succeeds), everything else is `0644`.
// embed.FS strips on-disk file modes, so we have to set them explicitly.
//
// Example:
//
//	srcZip := framework.ZipTestDataDir(t, "python/sourcepkg", "demo-src-pkg.zip")
//	ns.CreateFunction(t, ctx, framework.FunctionOptions{Src: srcZip, ...})
func ZipTestDataDir(t *testing.T, embedDir, archiveName string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), archiveName)
	out, err := os.Create(dst)
	require.NoErrorf(t, err, "ZipTestDataDir: create %q", dst)
	defer out.Close()

	zw := zip.NewWriter(out)
	walkErr := fs.WalkDir(testdata.FS, embedDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := testdata.FS.ReadFile(path)
		if err != nil {
			return err
		}
		base := filepath.Base(path)
		// Flat archive (no subdirs) like `zip -j`.
		fh := &zip.FileHeader{Name: base, Method: zip.Deflate}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(base, ".sh") {
			mode = 0o755
		}
		fh.SetMode(mode)
		w, err := zw.CreateHeader(fh)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	})
	require.NoErrorf(t, walkErr, "ZipTestDataDir: walk %q", embedDir)
	require.NoError(t, zw.Close(), "ZipTestDataDir: finalize zip")
	return dst
}

// ZipTestDataTree packs an embedded testdata directory into a zip archive
// preserving the relative path from `embedDir` (so subdirectories are kept).
// Mirrors `zip -r out.zip inner/` semantics where `inner` is the direct
// child of embedDir; entries land as `inner/<rel-path>`.
//
// Use this for fixtures that need their on-disk layout preserved (e.g.
// TensorFlow SavedModel directories, multi-package source trees).
//
// File modes: `*.sh` → 0755, others → 0644 (embed.FS strips file modes).
func ZipTestDataTree(t *testing.T, embedDir, archiveName string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), archiveName)
	out, err := os.Create(dst)
	require.NoErrorf(t, err, "ZipTestDataTree: create %q", dst)
	defer out.Close()

	zw := zip.NewWriter(out)
	require.NoError(t, fs.WalkDir(testdata.FS, embedDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := testdata.FS.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(embedDir, p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(rel, ".sh") {
			mode = 0o755
		}
		fh := &zip.FileHeader{Name: rel, Method: zip.Deflate}
		fh.SetMode(mode)
		w, err := zw.CreateHeader(fh)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}))
	require.NoError(t, zw.Close(), "ZipTestDataTree: finalize zip")
	return dst
}
