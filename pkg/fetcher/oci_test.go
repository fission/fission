// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fetcher

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// pushTestCodeImage pushes a single-layer image holding the given files to an
// in-memory plain-HTTP registry and returns the registry host, the image
// reference, and the image digest.
func pushTestCodeImage(t *testing.T, files map[string]string) (host, ref, digest string) {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for fname, body := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: fname, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)),
		}))
		_, err := tw.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	raw := buf.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(raw)), nil
	})
	require.NoError(t, err)
	img, err := mutate.AppendLayers(empty.Image, layer)
	require.NoError(t, err)

	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	host = u.Host
	ref = fmt.Sprintf("%s/code/hello:v1", host)
	parsed, err := name.ParseReference(ref, name.Insecure)
	require.NoError(t, err)
	require.NoError(t, remote.Write(parsed, img))

	d, err := img.Digest()
	require.NoError(t, err)
	return host, ref, d.String()
}

func newOCITestFetcher(t *testing.T) *Fetcher {
	t.Helper()
	return &Fetcher{
		logger:           logr.Discard(),
		sharedVolumePath: t.TempDir(),
		kubeClient:       k8sfake.NewSimpleClientset(),
		Info:             PodInfo{Name: "test-pod", Namespace: "fn-ns"},
	}
}

func ociPackage(image, digest string) *fv1.Package {
	return &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: "oci-pkg", Namespace: "fn-ns"},
		Spec: fv1.PackageSpec{
			Deployment: fv1.Archive{
				Type: fv1.ArchiveTypeOCI,
				OCI:  &fv1.OCIArchive{Image: image, Digest: digest},
			},
		},
		Status: fv1.PackageStatus{BuildStatus: fv1.BuildStatusNone},
	}
}

func TestFetchOCIDeployment(t *testing.T) {
	host, ref, _ := pushTestCodeImage(t, map[string]string{
		"hello.js": "module.exports = 'hi'",
	})
	t.Setenv("FETCHER_ALLOW_INSECURE_REGISTRIES", host)

	f := newOCITestFetcher(t)
	req := FunctionFetchRequest{
		FetchType: fv1.FETCH_DEPLOYMENT,
		Filename:  "userfunc",
	}

	code, err := f.Fetch(t.Context(), ociPackage(ref, ""), req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, code)

	got, err := os.ReadFile(filepath.Join(f.sharedVolumePath, "userfunc", "hello.js"))
	require.NoError(t, err)
	assert.Equal(t, "module.exports = 'hi'", string(got))

	// A second fetch must no-op via the existing storePath early-exit.
	code, err = f.Fetch(t.Context(), ociPackage(ref, ""), req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, code)
}

func TestFetchOCIBadDigest(t *testing.T) {
	host, ref, digest := pushTestCodeImage(t, map[string]string{
		"hello.js": "module.exports = 'hi'",
	})
	t.Setenv("FETCHER_ALLOW_INSECURE_REGISTRIES", host)

	wrong := "sha256:" + strings.Repeat("0", 64)
	require.NotEqual(t, digest, wrong)

	f := newOCITestFetcher(t)
	code, err := f.Fetch(t.Context(), ociPackage(ref, wrong), FunctionFetchRequest{
		FetchType: fv1.FETCH_DEPLOYMENT,
		Filename:  "userfunc",
	})
	require.Error(t, err)
	assert.Equal(t, http.StatusInternalServerError, code)
	_, statErr := os.Stat(filepath.Join(f.sharedVolumePath, "userfunc"))
	assert.True(t, os.IsNotExist(statErr), "failed fetch must not leave the store path behind")
}

func TestFetchOCIPinnedDigest(t *testing.T) {
	host, ref, digest := pushTestCodeImage(t, map[string]string{
		"hello.js": "pinned",
	})
	t.Setenv("FETCHER_ALLOW_INSECURE_REGISTRIES", host)

	f := newOCITestFetcher(t)
	code, err := f.Fetch(t.Context(), ociPackage(ref, digest), FunctionFetchRequest{
		FetchType: fv1.FETCH_DEPLOYMENT,
		Filename:  "userfunc",
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, code)
	got, err := os.ReadFile(filepath.Join(f.sharedVolumePath, "userfunc", "hello.js"))
	require.NoError(t, err)
	assert.Equal(t, "pinned", string(got))
}

func TestInsecureRegistriesFromEnv(t *testing.T) {
	t.Setenv("FETCHER_ALLOW_INSECURE_REGISTRIES", " reg-a.example.com:5000, ,reg-b.example.com ")
	assert.Equal(t, []string{"reg-a.example.com:5000", "reg-b.example.com"}, insecureRegistriesFromEnv())

	t.Setenv("FETCHER_ALLOW_INSECURE_REGISTRIES", "")
	assert.Nil(t, insecureRegistriesFromEnv())
}

// TestFetchOCIMidExtractionFailure covers the cleanup branch: an image whose
// layer contains a link entry fails DURING extraction (after the digest
// check), and neither the store path nor an orphaned tmp dir may remain.
func TestFetchOCIMidExtractionFailure(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := "ok"
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "good.py", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}))
	_, err := tw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "evil-link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}))
	require.NoError(t, tw.Close())
	raw := buf.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(raw)), nil
	})
	require.NoError(t, err)
	img, err := mutate.AppendLayers(empty.Image, layer)
	require.NoError(t, err)

	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	ref := fmt.Sprintf("%s/code/evil:v1", u.Host)
	parsed, err := name.ParseReference(ref, name.Insecure)
	require.NoError(t, err)
	require.NoError(t, remote.Write(parsed, img))
	t.Setenv("FETCHER_ALLOW_INSECURE_REGISTRIES", u.Host)

	f := newOCITestFetcher(t)
	code, err := f.Fetch(t.Context(), ociPackage(ref, ""), FunctionFetchRequest{
		FetchType: fv1.FETCH_DEPLOYMENT,
		Filename:  "userfunc",
	})
	require.Error(t, err)
	assert.Equal(t, http.StatusInternalServerError, code)

	_, statErr := os.Stat(filepath.Join(f.sharedVolumePath, "userfunc"))
	assert.True(t, os.IsNotExist(statErr), "failed fetch must not leave the store path behind")
	entries, err := os.ReadDir(f.sharedVolumePath)
	require.NoError(t, err)
	assert.Empty(t, entries, "failed fetch must clean up its tmp extraction dir")
}

// newRegistryHost starts an in-memory plain-HTTP OCI registry and returns
// its host (for push-mode tests that need only the registry, no image).
func newRegistryHost(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return u.Host
}

// stubStorageSvc serves the minimal storagesvc surface UploadReader needs.
func stubStorageSvc(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"stub-file-id"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func uploadViaHandler(t *testing.T, f *Fetcher, req *ArchiveUploadRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	require.NoError(t, err)
	rr := httptest.NewRecorder()
	f.UploadHandler(rr, httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(body)))
	return rr
}

// writeArtifactDir places a built deployment directory on the shared volume.
func writeArtifactDir(t *testing.T, f *Fetcher, name string) {
	t.Helper()
	dir := filepath.Join(f.sharedVolumePath, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.py"), []byte("def main(): pass\n"), 0o644))
}

// TestUploadHandlerOCIModes pins the producer's upload-handler mode table
// (RFC-0012): push success short-circuits the storagesvc upload entirely;
// strict push failure fails the request; fallback push failure rides the
// tarball path and reports the push error alongside.
func TestUploadHandlerOCIModes(t *testing.T) {
	t.Run("push success returns OCI and skips storage", func(t *testing.T) {
		f := newOCITestFetcher(t)
		writeArtifactDir(t, f, "artifact")
		host := newRegistryHost(t)
		rr := uploadViaHandler(t, f, &ArchiveUploadRequest{
			Filename: "artifact",
			OCIPush: &OCIPushSpec{
				Repository:    host + "/builds/pkg",
				InsecureHosts: []string{host},
			},
			// No StorageSvcUrl on purpose: a storage call would fail loudly.
		})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var resp ArchiveUploadResponse
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
		require.NotNil(t, resp.OCI)
		assert.Contains(t, resp.OCI.Image, host+"/builds/pkg:")
		assert.Contains(t, resp.OCI.Digest, "sha256:")
		assert.Empty(t, resp.ArchiveDownloadUrl, "OCI success must not upload a tarball")
		assert.Empty(t, resp.OCIPushError)
	})

	t.Run("published repository overrides the recorded name", func(t *testing.T) {
		f := newOCITestFetcher(t)
		writeArtifactDir(t, f, "artifact")
		host := newRegistryHost(t)
		rr := uploadViaHandler(t, f, &ArchiveUploadRequest{
			Filename: "artifact",
			OCIPush: &OCIPushSpec{
				Repository:          host + "/builds/pkg",
				PublishedRepository: "localhost:30500/builds/pkg",
				InsecureHosts:       []string{host},
			},
		})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var resp ArchiveUploadResponse
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
		require.NotNil(t, resp.OCI)
		assert.Contains(t, resp.OCI.Image, "localhost:30500/builds/pkg:",
			"the recorded reference must carry the consumption-side name")
		assert.True(t, strings.HasPrefix(resp.OCI.Digest, "sha256:"),
			"the digest pin must survive the prefix rewrite")

		// The prefix swap must NOT touch the digest: push the same artifact
		// without a published prefix and confirm the digest is byte-identical
		// (the load-bearing property — consumers pin on the digest).
		f2 := newOCITestFetcher(t)
		writeArtifactDir(t, f2, "artifact")
		rr2 := uploadViaHandler(t, f2, &ArchiveUploadRequest{
			Filename: "artifact",
			OCIPush:  &OCIPushSpec{Repository: host + "/builds/pkg", InsecureHosts: []string{host}},
		})
		require.Equal(t, http.StatusOK, rr2.Code, rr2.Body.String())
		var resp2 ArchiveUploadResponse
		require.NoError(t, json.Unmarshal(rr2.Body.Bytes(), &resp2))
		assert.Equal(t, resp2.OCI.Digest, resp.OCI.Digest,
			"identical content must yield an identical digest regardless of publishedPrefix")
	})

	t.Run("published prefix that is a superstring of the push repo replaces only the prefix", func(t *testing.T) {
		f := newOCITestFetcher(t)
		writeArtifactDir(t, f, "artifact")
		host := newRegistryHost(t)
		rr := uploadViaHandler(t, f, &ArchiveUploadRequest{
			Filename: "artifact",
			OCIPush: &OCIPushSpec{
				Repository:          host + "/builds/pkg",
				PublishedRepository: "localhost:30500/builds/pkg-mirror",
				InsecureHosts:       []string{host},
			},
		})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var resp ArchiveUploadResponse
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
		require.NotNil(t, resp.OCI)
		assert.True(t, strings.HasPrefix(resp.OCI.Image, "localhost:30500/builds/pkg-mirror:"),
			"only the leading push prefix should be rewritten, got %q", resp.OCI.Image)
	})

	t.Run("strict push failure fails the upload", func(t *testing.T) {
		f := newOCITestFetcher(t)
		writeArtifactDir(t, f, "artifact")
		rr := uploadViaHandler(t, f, &ArchiveUploadRequest{
			Filename: "artifact",
			OCIPush: &OCIPushSpec{
				Repository:        "127.0.0.1:1/builds/pkg",
				InsecureHosts:     []string{"127.0.0.1:1"},
				FallbackToStorage: false,
			},
		})
		assert.Equal(t, http.StatusInternalServerError, rr.Code)
		assert.Contains(t, rr.Body.String(), "error publishing")
	})

	t.Run("fallback push failure rides the tarball and reports the error", func(t *testing.T) {
		f := newOCITestFetcher(t)
		writeArtifactDir(t, f, "artifact")
		rr := uploadViaHandler(t, f, &ArchiveUploadRequest{
			Filename:       "artifact",
			StorageSvcUrl:  stubStorageSvc(t),
			ArchivePackage: true,
			OCIPush: &OCIPushSpec{
				Repository:        "127.0.0.1:1/builds/pkg",
				InsecureHosts:     []string{"127.0.0.1:1"},
				FallbackToStorage: true,
			},
		})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var resp ArchiveUploadResponse
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
		assert.Nil(t, resp.OCI)
		assert.NotEmpty(t, resp.OCIPushError, "the degradation must be reported")
		assert.Contains(t, resp.ArchiveDownloadUrl, "stub-file-id", "the tarball fallback must have uploaded")
	})
}
