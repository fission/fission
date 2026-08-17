// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0
package helm

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
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

	t.Run("renders with the authgen grant and pins SA, name, and type", func(t *testing.T) {
		t.Parallel()
		docs := render(t, "--set", "preUpgradeChecks.enabled=true,internalAuth.enabled=true,internalAuth.autoGenerate=true")

		policyDoc := docs.find("ValidatingAdmissionPolicy", "fission-fission-authgen-secret-guard")
		require.NotNil(t, policyDoc, "the secret-guard policy must render whenever the authgen create grant does")
		require.NotNil(t, docs.find("ValidatingAdmissionPolicyBinding", "fission-fission-authgen-secret-guard"),
			"a policy without its binding enforces nothing")

		policy := decodeAs[admissionregistrationv1.ValidatingAdmissionPolicy](t, policyDoc)
		require.NotNil(t, policy.Spec.FailurePolicy)
		assert.Equal(t, admissionregistrationv1.Fail, *policy.Spec.FailurePolicy, "the guard must fail closed")

		var conditions, validations strings.Builder
		for _, c := range policy.Spec.MatchConditions {
			conditions.WriteString(c.Expression)
		}
		assert.Contains(t, conditions.String(), "system:serviceaccount:fission:fission-preupgrade",
			"the guard must constrain exactly the hook ServiceAccount")

		for _, v := range policy.Spec.Validations {
			validations.WriteString(v.Expression + "\n")
		}
		assert.Contains(t, validations.String(), `object.metadata.name == "fission-internal-auth"`,
			"the hook may create only the master Secret by name")
		assert.Contains(t, validations.String(), "object.type == 'Opaque'",
			"typed Secrets (service-account-token) are the escalation primitive and must be forbidden")
	})

	// The invariant the whole fix rests on: the create-secrets grant and its
	// fence must appear and disappear together. Assert BOTH sides across every
	// value set, so widening the grant without the guard (or vice versa) fails.
	authgenRoleGrantsCreateSecrets := func(t *testing.T, docs manifests) bool {
		t.Helper()
		for _, r := range roles(t, docs) {
			if r.Kind != "Role" || r.Name != "fission-preupgrade-authgen" {
				continue
			}
			for _, rule := range r.Rules {
				if slices.Contains(rule.Resources, "secrets") && slices.Contains(rule.Verbs, "create") {
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
				grant := authgenRoleGrantsCreateSecrets(t, docs)
				fence := docs.find("ValidatingAdmissionPolicy", "fission-fission-authgen-secret-guard") != nil
				assert.Equalf(t, grant, fence,
					"the unfenced create-secrets grant (%v) and its VAP fence (%v) must render together", grant, fence)
			})
		}
	})
}
