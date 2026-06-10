// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fetcher

import (
	"archive/tar"
	"bytes"
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
