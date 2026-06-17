// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
)

// TestBuilderAuthEnvVars pins the builder /build channel's per-namespace key
// mount: it is sourced from the controller-owned tenant keys Secret, and the
// active key is REQUIRED only for a live tenant namespace under dynamic tenancy
// with auth enabled (so the kubelet gates pod start on the controller having
// provisioned it — race-free, mirroring the fetcher key). A non-tenant namespace
// keeps it optional so the pod still starts and falls back to master/pass-through.
func TestBuilderAuthEnvVars(t *testing.T) {
	const tenantNS = "builder-tenant-ns"
	resolver := utils.DefaultNSResolver()
	resolver.AddTenant(tenantNS)
	t.Cleanup(func() { resolver.RemoveTenant(tenantNS) })

	byName := func(vars []apiv1.EnvVar) map[string]apiv1.EnvVar {
		m := make(map[string]apiv1.EnvVar, len(vars))
		for _, v := range vars {
			m[v.Name] = v
		}
		return m
	}

	// Sourcing: the active builder key comes from the tenant keys Secret.
	t.Run("builder key sourced from the tenant keys Secret", func(t *testing.T) {
		t.Setenv("FISSION_DYNAMIC_NAMESPACES", "false")
		bk := byName(builderAuthEnvVars(tenantNS))["FISSION_BUILDER_KEY"]
		require.NotNil(t, bk.ValueFrom.SecretKeyRef)
		assert.Equal(t, fv1.TenantAuthKeysSecret, bk.ValueFrom.SecretKeyRef.Name)
		assert.Equal(t, fv1.TenantAuthBuilderKey, bk.ValueFrom.SecretKeyRef.Key)
	})

	tests := []struct {
		name         string
		dynamic      bool
		master       string
		namespace    string
		wantOptional bool
	}{
		{"dynamic + master + tenant requires the builder key", true, "master", tenantNS, false},
		{"dynamic + master + non-tenant stays optional", true, "master", "not-a-tenant", true},
		{"dynamic + auth disabled stays optional", true, "", tenantNS, true},
		{"non-dynamic stays optional", false, "master", tenantNS, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FISSION_DYNAMIC_NAMESPACES", strconv.FormatBool(tt.dynamic))
			t.Setenv("FISSION_INTERNAL_AUTH_SECRET", tt.master)
			bk := byName(builderAuthEnvVars(tt.namespace))["FISSION_BUILDER_KEY"]
			require.NotNil(t, bk.ValueFrom.SecretKeyRef.Optional)
			assert.Equal(t, tt.wantOptional, *bk.ValueFrom.SecretKeyRef.Optional)
		})
	}
}
