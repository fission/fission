//go:build integration

package common_test

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/test/integration/framework"
)

// TestArchiveCLI is the Go port of test_archive_cli.sh. Exercises the
// `fission ar` (archive) subcommands against a deployed storagesvc:
//
//	upload  → returns archive ID (cobra-aware output, captured normally)
//	list    → expects ID present (raw os.Stdout via fmt.Println, captured
//	          via CLICaptureStdout)
//	download → writes file to CWD with the bare file-ID basename
//	get-url → returns storagesvc-relative archive URL with %2F-encoded path
//	delete  → removes the archive
//	list    → expects ID absent
//
// The test runs in an isolated WithCWD so `download` doesn't pollute the
// working directory; CLICaptureStdout serializes against parallel CLI
// calls via cliMu so we don't accidentally swallow their stdout.
func TestArchiveCLI(t *testing.T) {
	// Note: cliMu serialization in CLICaptureStdout already protects
	// against parallel CLI stdout interleaving, but t.Parallel is fine
	// here: read-locked CLI calls running concurrently just block
	// briefly while our stdout-capturing call holds the write lock.
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)

	// Build a small archive: ~256K random + a tiny hello.py, zipped flat.
	workdir := t.TempDir()
	archiveDir := filepath.Join(workdir, "archive")
	require.NoError(t, os.Mkdir(archiveDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "hello.py"),
		[]byte("def main():\n    return \"Hello, world!\""), 0o644))
	rnd := make([]byte, 256*1024)
	_, _ = rand.Read(rnd)
	require.NoError(t, os.WriteFile(filepath.Join(archiveDir, "dynamically_generated_file"), rnd, 0o644))
	zipPath := filepath.Join(workdir, "test-deploy-pkg.zip")
	require.NoError(t, zipFlat(archiveDir, zipPath))

	// 1. Upload — cobra-aware output: "File successfully uploaded with ID: <id>"
	uploadOut := ns.CLI(t, ctx, "ar", "upload", "--name", zipPath)
	archiveID := strings.TrimSpace(strings.TrimPrefix(uploadOut, "File successfully uploaded with ID:"))
	require.NotEmptyf(t, archiveID, "could not parse archive ID from %q", uploadOut)

	// Cleanup deletes the archive on test failure. If the test body's
	// explicit delete (Phase 5 below) ran successfully, this redundant
	// delete returns HTTP 400 (already gone) — use the BestEffort
	// variant so the cleanup doesn't fail the test on that 400.
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer dcancel()
		_, _ = ns.CLICaptureStdoutBestEffort(t, dctx, "ar", "delete", "--id", archiveID)
	})

	// 2. List — expects archiveID present.
	listOut := ns.CLICaptureStdout(t, ctx, "ar", "list")
	require.Containsf(t, listOut, archiveID,
		"expected archive list output to contain ID %q, got:\n%s", archiveID, listOut)

	// 3. Download — writes to CWD with the bare file-ID basename.
	require.True(t, strings.HasPrefix(archiveID, "/fission/fission-functions/"),
		"archive ID has unexpected shape: %q", archiveID)
	fileID := strings.TrimPrefix(archiveID, "/fission/fission-functions/")
	ns.WithCWD(t, workdir, func() {
		ns.CLICaptureStdout(t, ctx, "ar", "download", "--id", archiveID)
	})
	downloaded := filepath.Join(workdir, fileID)
	_, statErr := os.Stat(downloaded)
	require.NoErrorf(t, statErr, "expected download to land at %s", downloaded)

	// 4. get-url — storagesvc URL path-escapes the ID, output line is
	//    "URL: http://<storage>/v1/archive?id=%2Ffission%2Ffission-functions%2F<fileID>"
	urlOut := ns.CLICaptureStdout(t, ctx, "ar", "get-url", "--id", archiveID)
	expectedSubstr := "/v1/archive?id=" + url.PathEscape(archiveID)
	require.Containsf(t, urlOut, expectedSubstr,
		"expected get-url output to contain %q, got: %s", expectedSubstr, urlOut)

	// 5. Delete + verify gone via list.
	ns.CLICaptureStdout(t, ctx, "ar", "delete", "--id", archiveID)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		out := ns.CLICaptureStdout(t, ctx, "ar", "list")
		assert.NotContainsf(c, out, archiveID,
			"expected archive list to exclude deleted ID %q", archiveID)
	}, 30*time.Second, 2*time.Second)
}

// zipFlat creates a zip at zipPath containing every file directly under
// srcDir (no subdirectory entries — mirrors `zip -j out.zip srcDir/*`).
func zipFlat(srcDir, zipPath string) error {
	out, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer out.Close()
	zw := zip.NewWriter(out)
	defer zw.Close()
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			return err
		}
		fh := &zip.FileHeader{Name: e.Name(), Method: zip.Deflate}
		fh.SetMode(0o644)
		w, err := zw.CreateHeader(fh)
		if err != nil {
			return err
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
	}
	return nil
}
