// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package framework

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/stretchr/testify/require"
)

// RequireRegistry skips the test unless a test registry is configured via
// FISSION_TEST_REGISTRY (host-reachable address the test pushes to, e.g.
// 127.0.0.1:5001) and FISSION_TEST_REGISTRY_INCLUSTER (the address cluster
// components pull from, e.g. test-registry.default.svc.cluster.local:5000).
// CI runs a registry:2 Deployment with a port-forward; locally:
//
//	docker run -d -p 5001:5000 registry:2
//	export FISSION_TEST_REGISTRY=127.0.0.1:5001
//	export FISSION_TEST_REGISTRY_INCLUSTER=<address your kind nodes can reach>
func RequireRegistry(t *testing.T) (hostAddr, inclusterAddr string) {
	t.Helper()
	hostAddr = os.Getenv("FISSION_TEST_REGISTRY")
	inclusterAddr = os.Getenv("FISSION_TEST_REGISTRY_INCLUSTER")
	if hostAddr == "" || inclusterAddr == "" {
		t.Skip("FISSION_TEST_REGISTRY / FISSION_TEST_REGISTRY_INCLUSTER not set; skipping OCI registry test")
	}
	return hostAddr, inclusterAddr
}

// PushCodeImage builds a single-layer FROM-scratch image whose filesystem is
// the given files and pushes it to the test registry over plain HTTP (with a
// short retry while the registry warms up). It returns the in-cluster image
// reference and the image digest.
func PushCodeImage(t *testing.T, hostAddr, inclusterAddr, repo, tag string, files map[string]string) (ref, digest string) {
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

	pushRef, err := name.ParseReference(fmt.Sprintf("%s/%s:%s", hostAddr, repo, tag), name.Insecure)
	require.NoError(t, err)
	deadline := time.Now().Add(30 * time.Second)
	for {
		err = remote.Write(pushRef, img)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	require.NoError(t, err, "pushing code image to test registry %s", hostAddr)

	d, err := img.Digest()
	require.NoError(t, err)
	return fmt.Sprintf("%s/%s:%s", inclusterAddr, repo, tag), d.String()
}
