// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/fission-cli/cmd"
	cliutil "github.com/fission/fission/pkg/fission-cli/util"
	ssclient "github.com/fission/fission/pkg/storagesvc/client"
	"github.com/fission/fission/test/integration/framework"
)

// TestArchiveNamespaceIsolation exercises the storagesvc archive content
// isolation (Phase 7) end-to-end against the deployed storagesvc: an archive
// uploaded by a namespace-scoped caller carries its owning namespace in the id,
// and a different tenant cannot read or delete it (404, never an existence
// oracle), while legacy/unscoped archives stay readable by anyone (grandfathered)
// and the master principal is unrestricted. It also satisfies the cross-namespace
// isolation negative-test backlog item.
//
// It drives storagesvc directly with derived per-namespace keys (the same keys
// the tenant fetcher and `package create` use), so it needs no dynamic tenancy —
// only HMAC enforcement. It skips when the master is unset (verifier pass-through).
func TestArchiveNamespaceIsolation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	master := f.InternalAuthSecret()
	if len(master) == 0 {
		t.Skip("FISSION_INTERNAL_AUTH_SECRET is unset; archive namespace isolation requires HMAC enforcement")
	}

	// Reach the deployed storagesvc the way the CLI does — a port-forward to
	// application=fission-storage in the release namespace.
	cmdClient, err := cmd.NewClient(cmd.ClientOptions{RestConfig: f.RestConfig(), Namespace: f.FissionNamespace()})
	require.NoError(t, err, "build cmd client")
	storageURL, err := cliutil.GetStorageURL(ctx, *cmdClient)
	require.NoError(t, err, "resolve storagesvc URL")
	urlStr := storageURL.String()

	nsA := "iso-a-" + framework.RandomID()
	nsB := "iso-b-" + framework.RandomID()
	clientA := ssclient.MakeClientNS(urlStr, master, nsA)
	clientB := ssclient.MakeClientNS(urlStr, master, nsB)
	masterClient := ssclient.MakeClient(urlStr, master)

	upload := func(c ssclient.ClientInterface, content string) string {
		t.Helper()
		fp := filepath.Join(t.TempDir(), "archive.bin")
		require.NoError(t, os.WriteFile(fp, []byte(content), 0o600))
		id, err := c.Upload(ctx, fp, nil)
		require.NoError(t, err)
		return id
	}
	info := func(c ssclient.ClientInterface, id string) int {
		t.Helper()
		resp, err := c.Info(ctx, id)
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}

	idA := upload(clientA, "archive-a")
	idB := upload(clientB, "archive-b")
	idLegacy := upload(masterClient, "archive-legacy")
	t.Cleanup(func() {
		cctx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		for _, id := range []string{idA, idB, idLegacy} {
			_ = masterClient.Delete(cctx, id) // best-effort; master is unrestricted
		}
	})

	// Scoped uploads carry the tenant marker; the legacy upload does not.
	assert.Contains(t, idA, "/_tenant_/"+nsA+"/", "tenant A upload must be namespace-scoped")
	assert.Contains(t, idB, "/_tenant_/"+nsB+"/", "tenant B upload must be namespace-scoped")
	assert.NotContains(t, idLegacy, "_tenant_", "master upload stays legacy/unscoped")

	// Each tenant reads its own archive; neither can see the other's (404).
	assert.Equal(t, http.StatusOK, info(clientA, idA))
	assert.Equal(t, http.StatusOK, info(clientB, idB))
	assert.Equal(t, http.StatusNotFound, info(clientA, idB), "tenant A must not see tenant B's archive")
	assert.Equal(t, http.StatusNotFound, info(clientB, idA), "tenant B must not see tenant A's archive")

	// Legacy archives are grandfathered; the master principal is unrestricted.
	assert.Equal(t, http.StatusOK, info(clientA, idLegacy), "legacy archive is grandfathered")
	assert.Equal(t, http.StatusOK, info(masterClient, idA))
	assert.Equal(t, http.StatusOK, info(masterClient, idB))

	// Cross-tenant DELETE is denied, and the archive survives (master still reads it).
	require.Error(t, clientA.Delete(ctx, idB), "tenant A must not delete tenant B's archive")
	assert.Equal(t, http.StatusOK, info(masterClient, idB), "B's archive must survive A's denied delete")

	// A tenant can download its own archive end-to-end.
	dst := filepath.Join(t.TempDir(), "dl.bin")
	require.NoError(t, clientA.Download(ctx, idA, dst))
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "archive-a", strings.TrimSpace(string(got)))
}
