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

	// The invariant the whole fix rests on: the create-secrets grant and its
	// fence must appear and disappear together. Assert BOTH sides across every
	// value set, so widening the grant without the guard (or vice versa) fails.
	authgenRoleGrantsCreateSecrets := func(docs []map[string]any) bool {
		for _, d := range docs {
			if d["kind"] != "Role" {
				continue
			}
			md, _ := d["metadata"].(map[string]any)
			if md == nil || md["name"] != "fission-preupgrade-authgen" {
				continue
			}
			for _, r := range asSlice(d["rules"]) {
				rule := r.(map[string]any)
				if containsStr(rule["resources"], "secrets") && containsStr(rule["verbs"], "create") {
					return true
				}
			}
		}
		return false
	}

	t.Run("fence and grant travel together across value sets", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name    string
			setArgs string
		}{
			// The chart forbids preUpgradeChecks.enabled=false alongside
			// autoGenerate (the master is provisioned by that Job), so
			// "generation on, hook off" is unrepresentable — the grant-absent
			// axis is exercised via generation instead.
			{"default in-cluster generation", "preUpgradeChecks.enabled=true,internalAuth.enabled=true,internalAuth.autoGenerate=true"},
			{"existingSecret disables both", "preUpgradeChecks.enabled=true,internalAuth.enabled=true,internalAuth.autoGenerate=true,internalAuth.existingSecret=org-master"},
			{"internalAuth off disables both", "crds.mode=none,preUpgradeChecks.enabled=false,internalAuth.enabled=false"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				docs := render(t, "--set", tc.setArgs)
				grant := authgenRoleGrantsCreateSecrets(docs)
				fence := find(docs, "ValidatingAdmissionPolicy", "fission-fission-authgen-secret-guard") != nil
				assert.Equalf(t, grant, fence,
					"the unfenced create-secrets grant (%v) and its VAP fence (%v) must render together", grant, fence)
			})
		}
	})
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func containsStr(v any, want string) bool {
	for _, e := range asSlice(v) {
		if s, ok := e.(string); ok && s == want {
			return true
		}
	}
	return false
}
