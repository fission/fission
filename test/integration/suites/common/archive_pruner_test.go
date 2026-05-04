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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/test/integration/framework"
)

// TestArchivePruner is the Go port of test_archive_pruner.sh.
//
// Creates two packages backed by deploy archives, deletes both packages,
// then waits for the storagesvc archive pruner controller to garbage-
// collect the now-orphaned archives. The pruner runs on a timer
// (configured at deploy time via PRUNE_INTERVAL); the bash version
// sleeps 300s and assumes pruning has happened by then.
//
// We use the same 5-minute baseline but poll for an additional 90s
// afterwards to absorb cluster scheduling jitter, mirroring the
// pattern used by other timer-driven controller tests.
func TestArchivePruner(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	runtime := f.Images().RequirePython(t)
	builder := f.Images().RequirePythonBuilder(t)

	ns := f.NewTestNamespace(t)
	envName := "py-pruner-" + ns.ID

	// Builder image isn't strictly needed (we use deploy archives only),
	// but the bash test created the env with a builder so we mirror.
	ns.CreateEnv(t, ctx, framework.EnvOptions{
		Name: envName, Image: runtime, Builder: builder,
	})

	// Build a deploy archive that exceeds fv1.ArchiveLiteralSizeLimit
	// (256K) — under that threshold the CLI inlines the bytes as a
	// literal Archive (no Spec.Deployment.URL set), and the pruner has
	// nothing to clean. Use 512K of random bytes (incompressible) so
	// the zipped archive is comfortably above the limit.
	zipPath := makePrunerDeployZip(t)

	// Create two packages with the same archive uploaded twice.
	pkg1 := "pkg-pruner-1-" + ns.ID
	pkg2 := "pkg-pruner-2-" + ns.ID
	ns.CreatePackage(t, ctx, framework.PackageOptions{
		Name: pkg1, Env: envName, Deploy: zipPath,
	})
	ns.CreatePackage(t, ctx, framework.PackageOptions{
		Name: pkg2, Env: envName, Deploy: zipPath,
	})

	// Capture the archive URLs before deletion (Spec.Deployment.URL is
	// the storagesvc archive download URL that the pruner targets).
	url1 := pkgArchiveID(t, ns.GetPackage(t, ctx, pkg1).Spec.Deployment.URL)
	url2 := pkgArchiveID(t, ns.GetPackage(t, ctx, pkg2).Spec.Deployment.URL)
	require.NotEmpty(t, url1, "pkg1 has no archive URL")
	require.NotEmpty(t, url2, "pkg2 has no archive URL")
	t.Logf("archive IDs: %q, %q", url1, url2)

	// Delete the packages — archives go orphan.
	require.NoError(t, f.FissionClient().CoreV1().Packages(ns.Name).Delete(ctx, pkg1, metav1.DeleteOptions{}))
	require.NoError(t, f.FissionClient().CoreV1().Packages(ns.Name).Delete(ctx, pkg2, metav1.DeleteOptions{}))

	// Snapshot: the archives should still be in the storagesvc list
	// immediately after deletion (pruner hasn't fired yet). This
	// catches the case where archives are accidentally pruned just
	// from being newly-created.
	listOut := ns.CLICaptureStdout(t, ctx, "ar", "list")
	require.Containsf(t, listOut, url1,
		"archive %q already missing right after pkg deletion (before pruner)", url1)
	require.Containsf(t, listOut, url2,
		"archive %q already missing right after pkg deletion (before pruner)", url2)

	// Wait the bash baseline 5 minutes, then poll for up to 90 more
	// seconds for both archives to be gone from the list.
	t.Logf("waiting for archive pruner (~5 min)")
	select {
	case <-time.After(5 * time.Minute):
	case <-ctx.Done():
		t.Fatalf("ctx cancelled during pruner wait: %v", ctx.Err())
	}

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		out := ns.CLICaptureStdout(t, ctx, "ar", "list")
		assert.NotContainsf(c, out, url1, "archive %q not pruned", url1)
		assert.NotContainsf(c, out, url2, "archive %q not pruned", url2)
	}, 90*time.Second, 5*time.Second)
}

// makePrunerDeployZip writes a zip containing a 512K random file under
// t.TempDir and returns its on-disk path. The size guarantees the CLI
// uploads to storagesvc (Spec.Deployment.URL set) instead of inlining
// as Archive literal bytes.
func makePrunerDeployZip(t *testing.T) string {
	t.Helper()
	workdir := t.TempDir()
	zipPath := filepath.Join(workdir, "deploy.zip")
	out, err := os.Create(zipPath)
	require.NoError(t, err)
	defer out.Close()
	zw := zip.NewWriter(out)
	defer zw.Close()

	// 512K random bytes — incompressible, so zip stays > 256K.
	rnd := make([]byte, 512*1024)
	_, _ = rand.Read(rnd)
	hdr := &zip.FileHeader{Name: "blob.bin", Method: zip.Deflate}
	hdr.SetMode(0o644)
	w, err := zw.CreateHeader(hdr)
	require.NoError(t, err)
	_, err = w.Write(rnd)
	require.NoError(t, err)

	// A tiny entrypoint so the archive is plausible as a Fission package
	// (the test never invokes it, but pruner inspects the storagesvc
	// archive directly so it doesn't matter).
	helloHdr := &zip.FileHeader{Name: "hello.py", Method: zip.Deflate}
	helloHdr.SetMode(0o644)
	hw, err := zw.CreateHeader(helloHdr)
	require.NoError(t, err)
	_, err = hw.Write([]byte("def main():\n    return \"Hello, world!\""))
	require.NoError(t, err)

	return zipPath
}

// pkgArchiveID extracts the storagesvc archive ID from a Package's
// Spec.Deployment.URL (which has the form
// http://.../v1/archive?id=<url-encoded-id>). Returns the decoded ID.
func pkgArchiveID(t *testing.T, deployURL string) string {
	t.Helper()
	if deployURL == "" {
		return ""
	}
	u, err := url.Parse(deployURL)
	require.NoErrorf(t, err, "parse package deployment URL %q", deployURL)
	id := u.Query().Get("id")
	require.NotEmptyf(t, id, "URL %q has no id query param", deployURL)
	// Sanity: the ID looks like /fission/fission-functions/<x>; bail if not.
	if !strings.HasPrefix(id, "/fission/fission-functions/") {
		t.Logf("unexpected archive ID shape: %q", id)
	}
	return id
}
