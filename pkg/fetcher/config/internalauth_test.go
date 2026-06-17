// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
)

// envByName indexes an env-var slice by name for assertion.
func envByName(t *testing.T, vars []apiv1.EnvVar) map[string]apiv1.EnvVar {
	t.Helper()
	m := make(map[string]apiv1.EnvVar, len(vars))
	for _, v := range vars {
		m[v.Name] = v
	}
	return m
}

// TestInternalAuthEnvVarsSources pins where each fetcher auth env var is sourced
// from and whether it is optional. The master vars come from the chart's
// master-bearing Secret; the per-namespace derived keys come from the
// controller-owned TenantAuthKeysSecret (a distinct name so it never collides
// with the chart copy). All are optional in the default single-namespace mode.
func TestInternalAuthEnvVarsSources(t *testing.T) {
	t.Setenv("FISSION_DYNAMIC_NAMESPACES", "false")
	vars := envByName(t, internalAuthEnvVars("team-a"))

	cases := []struct {
		env        string
		secretName string
		secretKey  string
	}{
		{"FISSION_INTERNAL_AUTH_SECRET", "fission-internal-auth", "secret"},
		{"FISSION_INTERNAL_AUTH_SECRET_OLD", "fission-internal-auth", "oldSecret"},
		{"FISSION_FETCHER_KEY", fv1.TenantAuthKeysSecret, fv1.TenantAuthFetcherKey},
		{"FISSION_FETCHER_KEY_OLD", fv1.TenantAuthKeysSecret, "fetcherKeyOld"},
		{"FISSION_STORAGE_KEY", fv1.TenantAuthKeysSecret, fv1.TenantAuthStorageKey},
		{"FISSION_STORAGE_KEY_OLD", fv1.TenantAuthKeysSecret, "storageKeyOld"},
	}
	for _, c := range cases {
		t.Run(c.env, func(t *testing.T) {
			v, ok := vars[c.env]
			require.True(t, ok, "env var present")
			require.NotNil(t, v.ValueFrom)
			require.NotNil(t, v.ValueFrom.SecretKeyRef)
			assert.Equal(t, c.secretName, v.ValueFrom.SecretKeyRef.Name, "secret name")
			assert.Equal(t, c.secretKey, v.ValueFrom.SecretKeyRef.Key, "secret key")
			require.NotNil(t, v.ValueFrom.SecretKeyRef.Optional)
			assert.True(t, *v.ValueFrom.SecretKeyRef.Optional, "optional in non-dynamic mode")
		})
	}
}

// TestInternalAuthEnvVarsFetcherKeyRequiredInDynamicMode pins the race-free
// contract: in dynamic tenancy with internal auth enabled, the fetcher key is
// REQUIRED for a TENANT namespace, so the kubelet blocks pod start until the
// tenant controller has provisioned that namespace's derived-key Secret. This
// makes the executor's version-aware ns-signing safe — a running pod is
// guaranteed to hold the key it will be signed with, never a stale master-only
// env that would 401. A NON-tenant namespace (e.g. a standalone builder
// namespace that never gets keys) keeps it optional so the pod still starts and
// falls back to master-derived signing — otherwise it would wedge forever. With
// auth disabled (no master) it stays optional so pass-through installs start.
func TestInternalAuthEnvVarsFetcherKeyRequiredInDynamicMode(t *testing.T) {
	const tenantNS = "tenant-ns-for-test"
	resolver := utils.DefaultNSResolver()
	resolver.AddTenant(tenantNS)
	t.Cleanup(func() { resolver.RemoveTenant(tenantNS) })

	tests := []struct {
		name         string
		dynamic      bool
		master       string
		namespace    string
		wantOptional bool
	}{
		{"dynamic + master + tenant namespace requires the fetcher key", true, "master-secret", tenantNS, false},
		{"dynamic + master + NON-tenant namespace stays optional (no wedge)", true, "master-secret", "not-a-tenant", true},
		{"dynamic + auth disabled keeps it optional", true, "", tenantNS, true},
		{"non-dynamic keeps it optional", false, "master-secret", tenantNS, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FISSION_DYNAMIC_NAMESPACES", boolStr(tt.dynamic))
			t.Setenv("FISSION_INTERNAL_AUTH_SECRET", tt.master)

			vars := envByName(t, internalAuthEnvVars(tt.namespace))
			fk := vars["FISSION_FETCHER_KEY"]
			require.NotNil(t, fk.ValueFrom.SecretKeyRef.Optional)
			assert.Equal(t, tt.wantOptional, *fk.ValueFrom.SecretKeyRef.Optional)

			// The storage key always stays optional: storagesvc dual-accepts a
			// master-derived signature, so an unprovisioned fetcher degrades
			// gracefully rather than failing to start.
			sk := vars["FISSION_STORAGE_KEY"]
			require.NotNil(t, sk.ValueFrom.SecretKeyRef.Optional)
			assert.True(t, *sk.ValueFrom.SecretKeyRef.Optional, "storage key stays optional")
		})
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
