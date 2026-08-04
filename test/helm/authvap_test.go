// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0
package helm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAuthgenSecretGuard pins the ValidatingAdmissionPolicy that fences the
// auth hook's necessarily-unfenced `create secrets` grant: RBAC cannot
// name-scope a plain POST, so without this policy the grant composes with the
// Role's fenced `get` into a ServiceAccount-token read (create the master
// name as a token-typed Secret, let the token controller fill it, read it
// back). The policy is the fence — it must render whenever the grant does,
// pin the hook SA by username, the master by name, and forbid typed Secrets.
func TestAuthgenSecretGuard(t *testing.T) {
	t.Parallel()

	find := func(docs []map[string]any, kind, name string) map[string]any {
		for _, d := range docs {
			if d["kind"] == kind {
				if md, ok := d["metadata"].(map[string]any); ok && md["name"] == name {
					return d
				}
			}
		}
		return nil
	}

	t.Run("renders with the authgen grant and pins SA, name, and type", func(t *testing.T) {
		t.Parallel()
		docs := render(t, "--set", "preUpgradeChecks.enabled=true,internalAuth.enabled=true,internalAuth.autoGenerate=true")

		policy := find(docs, "ValidatingAdmissionPolicy", "fission-fission-authgen-secret-guard")
		require.NotNil(t, policy, "the secret-guard policy must render whenever the authgen create grant does")
		require.NotNil(t, find(docs, "ValidatingAdmissionPolicyBinding", "fission-fission-authgen-secret-guard"),
			"a policy without its binding enforces nothing")

		spec := policy["spec"].(map[string]any)
		assert.Equal(t, "Fail", spec["failurePolicy"], "the guard must fail closed")

		var conditions, validations strings.Builder
		for _, c := range spec["matchConditions"].([]any) {
			conditions.WriteString(c.(map[string]any)["expression"].(string))
		}
		assert.Contains(t, conditions.String(), "system:serviceaccount:fission:fission-preupgrade",
			"the guard must constrain exactly the hook ServiceAccount")

		for _, v := range spec["validations"].([]any) {
			validations.WriteString(v.(map[string]any)["expression"].(string) + "\n")
		}
		assert.Contains(t, validations.String(), `object.metadata.name == "fission-internal-auth"`,
			"the hook may create only the master Secret by name")
		assert.Contains(t, validations.String(), "object.type == 'Opaque'",
			"typed Secrets (service-account-token) are the escalation primitive and must be forbidden")
	})

	t.Run("existingSecret name reaches the name pin", func(t *testing.T) {
		t.Parallel()
		// existingSecret disables in-cluster generation, so the grant AND the
		// guard both disappear — the fence tracks the grant exactly.
		docs := render(t, "--set", "preUpgradeChecks.enabled=true,internalAuth.enabled=true,internalAuth.autoGenerate=true,internalAuth.existingSecret=org-master")
		assert.Nil(t, find(docs, "ValidatingAdmissionPolicy", "fission-fission-authgen-secret-guard"),
			"with existingSecret the hook does not generate, so neither grant nor guard should render")
	})

	t.Run("absent when generation is off", func(t *testing.T) {
		t.Parallel()
		docs := render(t, "--set", "preUpgradeChecks.enabled=true,internalAuth.enabled=false")
		assert.Nil(t, find(docs, "ValidatingAdmissionPolicy", "fission-fission-authgen-secret-guard"))
	})
}
