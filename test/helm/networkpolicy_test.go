// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0
package helm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
)

// TestNetworkPolicySvcLabelsResolveToRealWorkloads is the fixed-name inventory
// guard for RFC-0029 phase 2 (naming / multi-instance).
//
// NetworkPolicies address workloads by a bare `svc:` pod label, which is a
// STRING that no template or Go constant forces to agree with the pod labels
// the Deployments actually carry. That makes a rename — exactly what phase 2
// does — able to orphan an allowlist entry, and the failure is silent: traffic
// is dropped, not rejected, and surfaces far away as `dial tcp <ip>:8889: i/o
// timeout` in an unrelated test.
//
// Asserting every referenced label is carried by some rendered pod template
// turns that into a build-time failure.
func TestNetworkPolicySvcLabelsResolveToRealWorkloads(t *testing.T) {
	// Every optional component is enabled on purpose. The allowlist legitimately
	// names workloads that only render behind a flag, so a default render would
	// report them as orphaned — the check is only meaningful when the workloads
	// it references actually exist.
	docs := render(t,
		"--set", "networkPolicy.enabled=true",
		"--set", "mcp.enabled=true", "--set", "mcp.allowInsecure=true",
		"--set", "canaryDeployment.enabled=true",
		"--set", "workflows.enabled=true",
		"--set", "agentRuntime.enabled=true", "--set", "agentRuntime.allowInsecure=true",
		// workflows and the agent runtime depend on the statestore; the chart
		// refuses to render otherwise (statestore/validate.yaml).
		"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
		"--set", "functionState.enabled=true",
	)

	// Every `svc:` label carried by a rendered pod template.
	known := map[string]bool{}
	for _, tmpl := range podTemplates(t, docs) {
		if v := tmpl.Labels["svc"]; v != "" {
			known[v] = true
		}
	}
	require.NotEmpty(t, known, "no pod template carried an svc label; the selector convention changed")

	var checked int
	policies := docs.ofKind("NetworkPolicy")
	for i := range policies {
		np := decodeAs[networkingv1.NetworkPolicy](t, &policies[i])
		for _, label := range podSelectorSvcLabels(np) {
			checked++
			assert.Truef(t, known[label],
				"NetworkPolicy %q references svc=%q, which no rendered pod template carries; "+
					"the policy would silently drop that traffic", np.Name, label)
		}
	}
	require.Positive(t, checked, "no NetworkPolicy svc selectors were checked; the guard is inert")
}

// podSelectorSvcLabels collects every `svc:` value a NetworkPolicy references —
// the workload it TARGETS plus everything its ingress allowlists admit. Both
// must name a workload that actually renders, which is what this feeds.
func podSelectorSvcLabels(np *networkingv1.NetworkPolicy) []string {
	var out []string
	if v := selectorSvcLabel(&np.Spec.PodSelector); v != "" {
		out = append(out, v)
	}
	return append(out, ingressSvcLabels(np)...)
}
