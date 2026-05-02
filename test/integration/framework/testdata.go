//go:build integration

package framework

import (
	"archive/zip"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

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
	if err != nil {
		t.Fatalf("WriteTestData: read embedded %q: %v", embedPath, err)
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, filepath.Base(embedPath))
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatalf("WriteTestData: write %q: %v", dst, err)
	}
	return dst
}

// ZipTestDataDir packs an embedded testdata directory into a flat zip archive
// (no subdirectory entries — matches the bash idiom `zip -jr out.zip dir/`)
// under t.TempDir() and returns the archive path. Use this for source-package
// fixtures the Fission builder consumes.
//
// Example:
//
//	srcZip := framework.ZipTestDataDir(t, "python/sourcepkg", "demo-src-pkg.zip")
//	ns.CreateFunction(t, ctx, framework.FunctionOptions{Src: srcZip, ...})
func ZipTestDataDir(t *testing.T, embedDir, archiveName string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), archiveName)
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("ZipTestDataDir: create %q: %v", dst, err)
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	err = fs.WalkDir(testdata.FS, embedDir, func(path string, d fs.DirEntry, err error) error {
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
		// Flat archive (no subdirs) like `zip -j`.
		w, err := zw.Create(filepath.Base(path))
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	})
	if err != nil {
		t.Fatalf("ZipTestDataDir: walk %q: %v", embedDir, err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("ZipTestDataDir: finalize zip: %v", err)
	}
	return dst
}
