// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0
package helm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/fission/fission/pkg/svcinfo"
)

// TestAgentRuntimeChartMaxContinuations checks Task 16's chart wiring: the
// agentruntime Deployment carries AGENT_MAX_CONTINUATIONS from
// agentRuntime.maxContinuations, defaulting to 1000 and following an
// explicit override (including the documented 0 = unlimited sentinel).
func TestAgentRuntimeChartMaxContinuations(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
		env := containerEnv(t, deployment(t, docs, svcinfo.SvcAgentRuntime))
		assert.Equal(t, "1000", env["AGENT_MAX_CONTINUATIONS"])
	})

	t.Run("override, including 0 = unlimited", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
			"--set", "agentRuntime.maxContinuations=0")
		env := containerEnv(t, deployment(t, docs, svcinfo.SvcAgentRuntime))
		assert.Equal(t, "0", env["AGENT_MAX_CONTINUATIONS"])
	})
}

// TestAgentRuntimeChartMaxSessionsDefault checks Task #5's chart wiring: the
// agentruntime Deployment carries AGENT_MAX_SESSIONS_DEFAULT from
// agentRuntime.maxSessions, defaulting to 1000 and following an explicit
// override (including the documented 0 = unlimited sentinel).
func TestAgentRuntimeChartMaxSessionsDefault(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
		env := containerEnv(t, deployment(t, docs, svcinfo.SvcAgentRuntime))
		assert.Equal(t, "1000", env["AGENT_MAX_SESSIONS_DEFAULT"])
	})

	t.Run("override, including 0 = unlimited", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
			"--set", "agentRuntime.maxSessions=0")
		env := containerEnv(t, deployment(t, docs, svcinfo.SvcAgentRuntime))
		assert.Equal(t, "0", env["AGENT_MAX_SESSIONS_DEFAULT"])
	})
}

// TestAgentRuntimeChartNetworkPolicy checks Task #8: an ingress NetworkPolicy
// for the agentruntime dispatcher renders only when both networkPolicy.enabled
// and agentRuntime.enabled, targets the agentruntime pods, and admits port
// 8894. It is intentionally permissive (no `from` allowlist) so the framework's
// portless integration forward is never dropped — see the template's comment.
func TestAgentRuntimeChartNetworkPolicy(t *testing.T) {
	t.Run("absent when networkPolicy disabled", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
		assert.Nil(t, docs.find("NetworkPolicy", "agentruntime"),
			"no agentruntime NetworkPolicy should render when networkPolicy.enabled is false")
	})

	t.Run("renders and admits port 8894 when enabled", func(t *testing.T) {
		docs := render(t,
			"--set", "networkPolicy.enabled=true",
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
		np := networkPolicy(t, docs, "agentruntime")

		assert.Equal(t, "agentruntime", np.Spec.PodSelector.MatchLabels["svc"],
			"the policy must target the agentruntime pods")
		require.Contains(t, np.Spec.PolicyTypes, networkingv1.PolicyTypeIngress)
		require.Len(t, np.Spec.Ingress, 1)

		var ports []int32
		for _, p := range np.Spec.Ingress[0].Ports {
			require.NotNil(t, p.Port)
			ports = append(ports, p.Port.IntVal)
		}
		assert.Contains(t, ports, int32(8894), "ingress must admit the agent runtime dispatch port 8894")
		assert.Empty(t, np.Spec.Ingress[0].From, "the policy is deliberately permissive: no from-allowlist")
	})

	// storagesvc netpol coverage: the workspace proxy (every PUT/GET/DELETE/
	// LIST) and the sweeper's archive-time prefix purge both call storagesvc
	// on port 8000 from the agentruntime pod. Missing this row is the
	// BLOCKER final-review.md finding 1 — every workspace call is silently
	// dropped under networkPolicy.enabled=true (the kind-ci profile forces
	// this on), which is exactly the "silent i/o timeout in CI" shape
	// TestWorkflowChart pins for the workflow engine's own two allowlists.
	t.Run("storagesvc networkpolicy admits svc:agentruntime", func(t *testing.T) {
		docs := render(t,
			"--set", "networkPolicy.enabled=true",
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
		ssNP := networkPolicy(t, docs, "storagesvc-allow-internal")
		assert.True(t, npAllowsFromSvc(ssNP, svcinfo.SvcAgentRuntime),
			"the workspace proxy and the sweeper's prefix purge both call storagesvc on port 8000; "+
				"a missing row is a silent i/o timeout in CI (networkPolicy.enabled under kind-ci)")
	})
}

// TestAgentRuntimeChartPoolRBAC guards Task 19's RBAC lockstep: pool
// introspection (GET /registry/pool) needs get/list/watch on
// endpointslices (discovery.k8s.io) + pods, granted through the single
// shared "agentruntime-rules" define — a missing grant here would silently
// degrade every replica's pool panel to a permanent 503 in any cluster
// whose RBAC came from this chart, with nothing else to catch it
// (pkg/agentruntime's own tests use a fake clientset, which enforces no
// RBAC at all). No "services" grant is expected: the informer watches
// EndpointSlices directly, PoolAPI lists Pods, and checkPoolRBAC's
// preflight names only endpointslices and pods.
//
// Static tenancy renders only the namespaced Role; dynamic/cluster tenancy
// additionally renders the cluster-wide ClusterRole (tenant-controller/
// dynamic-cluster-roles.yaml) — both must carry the same grant, since both
// share the one "agentruntime-rules" define.
func TestAgentRuntimeChartPoolRBAC(t *testing.T) {
	wantChecks := []struct{ group, resource string }{
		{"discovery.k8s.io", "endpointslices"},
		{"", "pods"},
	}

	t.Run("Role (static tenancy)", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
		found := false
		for _, role := range roles(t, docs) {
			if role.Kind != "Role" || !strings.Contains(role.Name, "agentruntime") {
				continue
			}
			found = true
			for _, chk := range wantChecks {
				verbs := verbsFor(role.Rules, chk.group, chk.resource)
				for _, want := range []string{"get", "list", "watch"} {
					assert.Truef(t, verbs[want], "Role %q must grant %q on %s/%s (have %v)", role.Name, want, chk.group, chk.resource, verbs)
				}
			}
		}
		require.True(t, found, "no agentruntime Role was rendered")
	})

	t.Run("ClusterRole (dynamic tenancy)", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
			"--set", "tenancy.mode=dynamic")
		found := false
		for _, role := range roles(t, docs) {
			if role.Kind != "ClusterRole" || !strings.Contains(role.Name, "agentruntime") {
				continue
			}
			found = true
			for _, chk := range wantChecks {
				verbs := verbsFor(role.Rules, chk.group, chk.resource)
				for _, want := range []string{"get", "list", "watch"} {
					assert.Truef(t, verbs[want], "ClusterRole %q must grant %q on %s/%s (have %v)", role.Name, want, chk.group, chk.resource, verbs)
				}
			}
		}
		require.True(t, found, "no agentruntime ClusterRole was rendered under tenancy.mode=dynamic")
	})
}

// TestAgentRuntimeChartAuthGates guards the render-time auth gate in
// deployment.yaml (the {{ else }} `required` clause guarding
// agentRuntime.enabled) and its positive counterpart: with
// authentication.enabled, the Deployment must carry JWT_SIGNING_KEY sourced
// from the shared authentication secret and must NOT also carry
// AGENT_ALLOW_INSECURE — the two are mutually exclusive branches of the same
// {{- if }}/{{- else if }}/{{- else }} conditional, so both ends of that
// branch are worth pinning together. Without EITHER authentication.enabled
// or agentRuntime.allowInsecure, the chart must refuse to render rather than
// silently deploy the dispatch endpoint unauthenticated.
func TestAgentRuntimeChartAuthGates(t *testing.T) {
	t.Run("authentication.enabled renders JWT_SIGNING_KEY, not AGENT_ALLOW_INSECURE", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true",
			"--set", "authentication.enabled=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
		c := firstContainer(t, deployment(t, docs, svcinfo.SvcAgentRuntime))

		var jwtEnv *corev1.EnvVar
		for i := range c.Env {
			if c.Env[i].Name == "JWT_SIGNING_KEY" {
				jwtEnv = &c.Env[i]
				break
			}
		}
		// JWT_SIGNING_KEY is a valueFrom secretKeyRef, not a literal value —
		// containerEnv (helmrender_test.go) deliberately skips valueFrom
		// entries, so this walks Env directly rather than going through it.
		require.NotNil(t, jwtEnv, "JWT_SIGNING_KEY must be rendered when authentication.enabled")
		require.NotNilf(t, jwtEnv.ValueFrom, "JWT_SIGNING_KEY must be sourced from a secret, not a literal value (got %+v)", jwtEnv)
		require.NotNil(t, jwtEnv.ValueFrom.SecretKeyRef, "JWT_SIGNING_KEY must come from a secretKeyRef")
		assert.Equal(t, "jwtSigningKey", jwtEnv.ValueFrom.SecretKeyRef.Key)

		for _, e := range c.Env {
			assert.NotEqualf(t, "AGENT_ALLOW_INSECURE", e.Name,
				"AGENT_ALLOW_INSECURE must not render alongside authentication.enabled")
		}
	})

	t.Run("neither authentication nor allowInsecure refuses to render", func(t *testing.T) {
		_, err := renderErr(t,
			"--set", "agentRuntime.enabled=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
		require.Error(t, err, "agentRuntime.enabled with no auth stance chosen must fail the render")
		assert.Contains(t, err.Error(), "agentRuntime.enabled requires authentication.enabled",
			"the failure must name the value the operator has to change")
	})

	t.Run("enabled without statestore refuses to render (statestore gate)", func(t *testing.T) {
		// allowInsecure=true so the auth gate above cannot fire first and mask
		// which gate actually failed — the same "wrong refusal" trap
		// internalauth_chart_test.go documents.
		_, err := renderErr(t,
			"--set", "agentRuntime.enabled=true",
			"--set", "agentRuntime.allowInsecure=true")
		require.Error(t, err, "agentRuntime.enabled without a statestore must fail the render")
		assert.Contains(t, err.Error(), "a statestore-dependent feature",
			"the failure must name the statestore gate, not some other refusal")
	})
}

// TestAgentRuntimeChartExecutorURL checks Task 4's chart wiring: the executor
// Deployment carries AGENT_RUNTIME_URL, pointing at the agentruntime
// ClusterIP Service, when agentRuntime.enabled — and carries no such env var
// when the feature is off, so an executor pod is never handed a URL for a
// dispatcher that isn't deployed.
func TestAgentRuntimeChartExecutorURL(t *testing.T) {
	t.Run("present when agentRuntime.enabled", func(t *testing.T) {
		docs := render(t,
			"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
		env := containerEnv(t, deployment(t, docs, svcinfo.SvcExecutor))
		assert.Equal(t, "http://agentruntime.fission:8894", env["AGENT_RUNTIME_URL"],
			"executor must be handed the agentruntime dispatcher's ClusterIP Service URL")
	})

	t.Run("absent when agentRuntime disabled", func(t *testing.T) {
		docs := render(t)
		env := containerEnv(t, deployment(t, docs, svcinfo.SvcExecutor))
		_, ok := env["AGENT_RUNTIME_URL"]
		assert.False(t, ok, "AGENT_RUNTIME_URL must not render when agentRuntime.enabled is false")
	})
}

// TestAgentRuntimeChartStoragesvcURL checks the workspace-purge slice's chart
// wiring: the agentruntime Deployment always carries STORAGESVC_URL, mirroring
// buildermgr's own --storageSvcUrl VALUE
// (charts/fission-all/templates/buildermgr/deployment.yaml) as an ENV rather
// than a container arg. Unlike AGENT_RUNTIME_URL on the executor (which is
// gated on agentRuntime.enabled), storagesvc is a core, always-deployed
// component with no enabled gate to condition on, so this env is unconditional.
func TestAgentRuntimeChartStoragesvcURL(t *testing.T) {
	docs := render(t,
		"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
		"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
	env := containerEnv(t, deployment(t, docs, svcinfo.SvcAgentRuntime))
	assert.Equal(t, "http://storagesvc.fission", env["STORAGESVC_URL"],
		"agentruntime's workspace routes must be handed storagesvc's ClusterIP Service URL, mirroring buildermgr's --storageSvcUrl value")
}

// TestAgentRuntimeChartInternalAuthEnvs is a verify-only assertion (no chart
// change needed): the agentruntime Deployment already carries
// FISSION_INTERNAL_AUTH_SECRET under default values, wired via
// internalAuth.envs (deployment.yaml:88) and gated on internalAuth.enabled
// (default true), not on agentRuntime.enabled.
func TestAgentRuntimeChartInternalAuthEnvs(t *testing.T) {
	docs := render(t,
		"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
		"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")
	c := firstContainer(t, deployment(t, docs, svcinfo.SvcAgentRuntime))

	var secretEnv *corev1.EnvVar
	for i := range c.Env {
		if c.Env[i].Name == "FISSION_INTERNAL_AUTH_SECRET" {
			secretEnv = &c.Env[i]
			break
		}
	}
	// FISSION_INTERNAL_AUTH_SECRET is a valueFrom secretKeyRef, not a literal
	// value — containerEnv deliberately skips valueFrom entries, so this
	// walks Env directly rather than going through it.
	require.NotNil(t, secretEnv, "FISSION_INTERNAL_AUTH_SECRET must be rendered under default values (internalAuth.enabled defaults true)")
	require.NotNil(t, secretEnv.ValueFrom, "FISSION_INTERNAL_AUTH_SECRET must be sourced from a secret, not a literal value")
	require.NotNil(t, secretEnv.ValueFrom.SecretKeyRef)
	assert.Equal(t, "secret", secretEnv.ValueFrom.SecretKeyRef.Key)
}
