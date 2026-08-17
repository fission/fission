// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0
package helm

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

// TestInternalAuthExistingSecret pins RFC-0029's `internalAuth.existingSecret`
// contract, whose failure mode is silent.
//
// Every consumer must resolve ONE name. The secretKeyRef is mounted optional so
// rotation can drop a key without forcing an empty render — which also means a
// pod whose reference points at a Secret that does not exist starts happily
// with the env var absent, and then 401s on every archive fetch and builder
// upload with nothing naming the Secret as the cause.
//
// The Go side resolves the same name from FISSION_INTERNAL_AUTH_SECRET_NAME
// (fv1.InternalAuthSecretName), so the chart must set it to whatever it wired
// into the secretKeyRefs.
func TestInternalAuthExistingSecret(t *testing.T) {
	const defaultName = "fission-internal-auth"

	t.Run("default: the chart renders the master and points at it", func(t *testing.T) {
		docs := render(t)
		assert.Positive(t, docs.countByKindName("Secret", defaultName),
			"with no existingSecret the chart must generate the master itself")
		names := internalAuthEnvNames(t, docs)
		require.NotEmpty(t, names,
			"no internal-auth secretKeyRef was rendered; the loop below would assert nothing")
		for _, name := range names {
			assert.Equal(t, defaultName, name)
		}
	})

	t.Run("existingSecret: the chart renders no master and points at theirs", func(t *testing.T) {
		docs := render(t, "--set", "internalAuth.existingSecret=my-master")
		assert.Zero(t, docs.countByKindName("Secret", defaultName),
			"the chart must not generate a master when the operator supplies one")
		names := internalAuthEnvNames(t, docs)
		require.NotEmpty(t, names, "no internal-auth secretKeyRef was rendered")
		for _, name := range names {
			assert.Equal(t, "my-master", name,
				"every consumer must read the operator's Secret, not the chart's default")
		}
	})

	// The env var is what the Go side reads. If it disagrees with the
	// secretKeyRefs above, the two halves resolve different Secrets.
	t.Run("the name env var matches the secretKeyRefs", func(t *testing.T) {
		for _, tc := range []struct {
			args []string
			want string
		}{
			{nil, defaultName},
			{[]string{"--set", "internalAuth.existingSecret=my-master"}, "my-master"},
		} {
			vals := envValues(t, render(t, tc.args...), "FISSION_INTERNAL_AUTH_SECRET_NAME")
			require.NotEmptyf(t, vals, "FISSION_INTERNAL_AUTH_SECRET_NAME must be set (%v)", tc.args)
			for _, v := range vals {
				assert.Equal(t, tc.want, v)
			}
		}
	})
}

// TestInternalAuthAutoGenerate pins RFC-0029 §10's generation path, including
// the tenancy rule that decides where the master may land.
func TestInternalAuthAutoGenerate(t *testing.T) {
	const master = "fission-internal-auth"

	t.Run("off by default: the template still renders the master", func(t *testing.T) {
		docs := render(t)
		assert.Positive(t, docs.countByKindName("Secret", master),
			"autoGenerate defaults off, so existing installs must be untouched")
		assert.Zero(t, docs.countByKindName("Role", "fission-preupgrade-authgen"),
			"no generation means no read grant")
	})

	t.Run("on: the template stops rendering and the hook is wired", func(t *testing.T) {
		docs := render(t, "--set", "internalAuth.autoGenerate=true")
		assert.Zero(t, docs.countByKindName("Secret", master),
			"generation moves in-cluster, so the template must render no master — "+
				"the retention hook is what stops Helm pruning the live one")
		vals := envValues(t, docs, "GENERATE_AUTH_SECRET")
		require.NotEmpty(t, vals, "the hook must be told which Secret to provision")
		for _, v := range vals {
			assert.Equal(t, master, v)
		}
	})

	// The rule the whole design turns on: under dynamic/cluster tenancy the
	// master must NOT reach tenant namespaces — they get controller-owned
	// derived keys, and a master there would defeat the isolation end state.
	t.Run("static tenancy fans out; dynamic does not", func(t *testing.T) {
		static := render(t,
			"--set", "internalAuth.autoGenerate=true",
			"--set", "additionalFissionNamespaces={fission-fn}")
		staticNS := envValues(t, static, "GENERATE_AUTH_SECRET_NAMESPACES")
		require.NotEmpty(t, staticNS)
		assert.Contains(t, staticNS[0], "fission-fn",
			"static tenancy replicates the master: kubelet cannot resolve a cross-namespace secretKeyRef")

		dynamic := render(t,
			"--set", "internalAuth.autoGenerate=true",
			"--set", "tenancy.mode=dynamic",
			"--set", "additionalFissionNamespaces={fission-fn}")
		dynNS := envValues(t, dynamic, "GENERATE_AUTH_SECRET_NAMESPACES")
		require.NotEmpty(t, dynNS)
		assert.NotContains(t, dynNS[0], "fission-fn",
			"under dynamic tenancy the master must stay in the release namespace: "+
				"tenant namespaces get controller-owned derived keys and must never hold the master")
	})

	// The retention set must cover the Secret Helm PREVIOUSLY managed, and
	// setting existingSecret is precisely what drops that object from the
	// rendered manifest. Scoping retention to "we are not using an existing
	// secret" destroys the master in the most natural adoption there is:
	// point existingSecret at the chart-generated Secret to take ownership of
	// it, and Helm prunes a Secret that is still in use.
	t.Run("retention still covers the master when existingSecret adopts it", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			args []string
		}{
			{"default", nil},
			{"adopting the chart-generated secret", []string{"--set", "internalAuth.existingSecret=" + master}},
			{"an operator-owned secret under another name", []string{"--set", "internalAuth.existingSecret=my-own"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				vals := envValues(t, render(t, tc.args...), "ADOPT_SECRETS")
				require.NotEmpty(t, vals, "the retention hook must render")
				assert.Containsf(t, vals[0], master,
					"the chart-generated master must stay in the retention set (%s), "+
						"or Helm prunes it the moment the template stops rendering it", tc.name)
			})
		}
	})

	// autoGenerate moves provisioning into the hook Job, so the template stops
	// rendering the Secret. If the Job is also switched off, NOTHING provisions
	// the master and every control-plane pod wedges on a required secretKeyRef
	// — a whole install that comes up dead from two values that each look
	// reasonable alone. The chart must refuse to render, exactly as it already
	// does for crds.mode=hook with the same Job disabled.
	t.Run("autoGenerate without the hook Job refuses to render", func(t *testing.T) {
		_, err := renderErr(t,
			"--set", "internalAuth.autoGenerate=true",
			"--set", "preUpgradeChecks.enabled=false",
			// Otherwise the pre-existing crds.mode guard fires first and we
			// would be asserting the wrong refusal.
			"--set", "crds.mode=none")
		require.Error(t, err, "autoGenerate with preUpgradeChecks disabled provisions no master at all")
		assert.Contains(t, err.Error(), "preUpgradeChecks.enabled must be true",
			"the failure must name the value the operator has to change")
	})

	// The read grant is the one concession this design makes; it must not widen
	// beyond the single object that needs it.
	//
	// The split between the two rules is load-bearing, not cosmetic. RBAC
	// matches resourceNames against the name in the REQUEST PATH, and a POST
	// carries the object name in the body instead — so a create rule fenced by
	// resourceNames matches nothing and the hook fails Forbidden. Fencing
	// create looks safer and is actually inert, which is precisely the kind of
	// mistake that survives review, so pin both halves.
	t.Run("the generation grant is fenced to the master alone", func(t *testing.T) {
		docs := render(t, "--set", "internalAuth.autoGenerate=true")
		var checked, sawGet, sawCreate int
		for _, role := range roles(t, docs) {
			if role.Kind != "Role" || role.Name != "fission-preupgrade-authgen" {
				continue
			}
			checked++
			for _, rule := range role.Rules {
				names := rule.ResourceNames
				verbs := rule.Verbs
				for _, v := range verbs {
					assert.NotEqual(t, "list", v, "list would let this identity enumerate every Secret")
					assert.NotEqual(t, "delete", v)
				}
				if slices.Contains(verbs, "create") {
					sawCreate++
					assert.Empty(t, names,
						"create must NOT carry resourceNames: the name is in the POST body, "+
							"not the path, so a fenced rule never matches and generation fails Forbidden")
					assert.Equal(t, []string{"create"}, verbs,
						"the unfenced rule must carry create and nothing else — "+
							"any read/write verb here would apply to every Secret in the namespace")
				} else {
					sawGet++
					require.Len(t, names, 1, "the read grant must name exactly one Secret")
					assert.Equal(t, master, names[0])
				}
			}
		}
		require.Positive(t, checked, "no generation Role was rendered; the guard is inert")
		assert.Positive(t, sawGet, "generation needs a name-scoped get to tell provisioned from absent")
		assert.Positive(t, sawCreate, "generation needs a create it can actually use")
	})
}

// internalAuthEnvNames collects the Secret names every FISSION_INTERNAL_AUTH_SECRET
// secretKeyRef points at, across every rendered pod template.
func internalAuthEnvNames(t *testing.T, docs manifests) []string {
	t.Helper()
	var out []string
	forEachContainerEnv(t, docs, func(e corev1.EnvVar) {
		if e.Name != "FISSION_INTERNAL_AUTH_SECRET" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
			return
		}
		out = append(out, e.ValueFrom.SecretKeyRef.Name)
	})
	return out
}

// envValues collects the literal values of a named env var across every
// rendered pod template.
func envValues(t *testing.T, docs manifests, envName string) []string {
	t.Helper()
	var out []string
	forEachContainerEnv(t, docs, func(e corev1.EnvVar) {
		if e.Name == envName && e.ValueFrom == nil {
			out = append(out, e.Value)
		}
	})
	return out
}
