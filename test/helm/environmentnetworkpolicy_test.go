// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0
package helm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// TestEnvironmentNetworkPolicyAbsentByDefault checks the knob is off by
// default (values.yaml environmentNetworkPolicy.enabled: false), matching
// the ingress-only networkPolicy knob's own default.
func TestEnvironmentNetworkPolicyAbsentByDefault(t *testing.T) {
	docs := render(t)
	assert.Nil(t, docs.find("NetworkPolicy", "environment-egress"),
		"no environment-egress NetworkPolicy should render when environmentNetworkPolicy.enabled is unset")
}

// TestEnvironmentNetworkPolicyIndependentOfIngressKnob checks the egress
// knob renders on its own, independent of the ingress-only networkPolicy
// flag — the two are documented as independently enableable in values.yaml.
func TestEnvironmentNetworkPolicyIndependentOfIngressKnob(t *testing.T) {
	docs := render(t, "--set", "environmentNetworkPolicy.enabled=true")
	require.NotNil(t, docs.find("NetworkPolicy", "environment-egress"),
		"environment-egress must render with environmentNetworkPolicy.enabled=true alone")
	assert.Nil(t, docs.find("NetworkPolicy", "functionpods-allow-internal"),
		"the unrelated ingress-only networkPolicy knob must stay off")
}

// TestEnvironmentNetworkPolicyNamespaceScope pins the failure mode the
// template's own comment names: NetworkPolicy podSelector only matches pods
// in the policy's own namespace, so this must render into
// fission-function-ns (functionNamespace if set, else defaultNamespace),
// never the release namespace — otherwise it silently becomes a no-op
// whenever functionNamespace differs (e.g. the kind-ci profile, which sets
// functionNamespace=fission-function while the release lives in "fission").
func TestEnvironmentNetworkPolicyNamespaceScope(t *testing.T) {
	t.Run("defaultNamespace when functionNamespace unset", func(t *testing.T) {
		docs := render(t, "--set", "environmentNetworkPolicy.enabled=true")
		np := networkPolicy(t, docs, "environment-egress")
		assert.Equal(t, "default", np.Namespace, "must fall back to defaultNamespace")
	})

	t.Run("functionNamespace when set", func(t *testing.T) {
		docs := render(t,
			"--set", "environmentNetworkPolicy.enabled=true",
			"--set", "functionNamespace=fission-function")
		np := networkPolicy(t, docs, "environment-egress")
		assert.Equal(t, "fission-function", np.Namespace,
			"must follow functionNamespace, not the release namespace — a mismatch here "+
				"silently no-ops the policy since podSelector only matches its own namespace")
	})
}

// TestEnvironmentNetworkPolicyCoversAdditionalFissionNamespaces pins the
// STATIC-tenancy multi-namespace case: function pods for functions in
// additionalFissionNamespaces live in their own namespace (pkg/utils
// NamespaceResolver.GetFunctionNS maps only the DEFAULT namespace through
// functionNamespace), so a single copy of this policy would silently leave
// those pods' egress unenforced. The template must render one copy per
// namespace in the set — mirroring router/role-dataplane.yaml's own range —
// and none of them in the release namespace, which is not itself a function
// namespace.
func TestEnvironmentNetworkPolicyCoversAdditionalFissionNamespaces(t *testing.T) {
	t.Run("functionNamespace unset: defaultNamespace plus additional", func(t *testing.T) {
		docs := render(t,
			"--set", "environmentNetworkPolicy.enabled=true",
			"--set", "additionalFissionNamespaces[0]=team-a",
			"--set", "additionalFissionNamespaces[1]=team-b")

		policies := docs.findAllNamed("NetworkPolicy", "environment-egress")
		var namespaces []string
		for _, p := range policies {
			namespaces = append(namespaces, p.Namespace)
		}
		assert.ElementsMatch(t, []string{"default", "team-a", "team-b"}, namespaces,
			"must render one environment-egress policy per function namespace")

		for _, ns := range []string{"default", "team-a", "team-b"} {
			require.NotNilf(t, docs.findInNamespace("NetworkPolicy", "environment-egress", ns),
				"expected an environment-egress policy in namespace %q", ns)
		}
		assert.Nil(t, docs.findInNamespace("NetworkPolicy", "environment-egress", "fission"),
			"the release namespace is not a function namespace and must not get a copy")
	})

	t.Run("functionNamespace set: functionNamespace plus additional, defaultNamespace excluded", func(t *testing.T) {
		docs := render(t,
			"--set", "environmentNetworkPolicy.enabled=true",
			"--set", "functionNamespace=fission-function",
			"--set", "additionalFissionNamespaces[0]=team-a")

		policies := docs.findAllNamed("NetworkPolicy", "environment-egress")
		var namespaces []string
		for _, p := range policies {
			namespaces = append(namespaces, p.Namespace)
		}
		assert.ElementsMatch(t, []string{"fission-function", "team-a"}, namespaces,
			"functionNamespace replaces the default-namespace entry; additionalFissionNamespaces "+
				"still get their own copy")
	})
}

// TestEnvironmentNetworkPolicySelectorMatchesTheEnvironmentLabelConstant is
// the drift tripwire: the podSelector's label key is a bare YAML string in
// the template, not forced to agree with the Go constant poolmgr/newdeploy
// actually stamp on pods (fv1.ENVIRONMENT_NAME). A rename of that constant
// without updating the template silently un-scopes this policy — every
// environment pod stops being selected — and the failure is invisible until
// egress traffic is unexpectedly wide open in a cluster that thought it
// wasn't.
func TestEnvironmentNetworkPolicySelectorMatchesTheEnvironmentLabelConstant(t *testing.T) {
	docs := render(t, "--set", "environmentNetworkPolicy.enabled=true")
	np := networkPolicy(t, docs, "environment-egress")

	require.NotNil(t, np.Spec.PodSelector.MatchExpressions)
	var found bool
	for _, expr := range np.Spec.PodSelector.MatchExpressions {
		if expr.Key == fv1.ENVIRONMENT_NAME && expr.Operator == metav1.LabelSelectorOpExists {
			found = true
		}
	}
	assert.True(t, found,
		"podSelector must match on the presence of the %q label (fv1.ENVIRONMENT_NAME), "+
			"the label poolmgr and newdeploy stamp on every environment pod", fv1.ENVIRONMENT_NAME)
}

// TestEnvironmentNetworkPolicyDefaultPosture pins the default egress shape:
// Egress-only policyType, a deny-list ipBlock rule covering RFC1918 +
// link-local (which subsumes the 169.254.169.254 cloud metadata IP), and a
// cluster-DNS allow rule present by default.
func TestEnvironmentNetworkPolicyDefaultPosture(t *testing.T) {
	docs := render(t, "--set", "environmentNetworkPolicy.enabled=true")
	np := networkPolicy(t, docs, "environment-egress")

	require.Equal(t, []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, np.Spec.PolicyTypes)

	peer := findIPBlockPeer(t, np, "0.0.0.0/0")
	assert.ElementsMatch(t, []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
	}, peer.IPBlock.Except, "RFC1918 + link-local must all be excluded from the allowed CIDR")

	assert.True(t, hasDNSRule(np), "cluster-DNS egress rule must be present by default (allowClusterDNS defaults true)")
}

// TestEnvironmentNetworkPolicyAllowClusterDNSFalseDropsTheDNSRule checks the
// knob actually removes the DNS rule rather than just being decorative,
// leaving only the RFC1918/link-local deny-list rule.
func TestEnvironmentNetworkPolicyAllowClusterDNSFalseDropsTheDNSRule(t *testing.T) {
	docs := render(t,
		"--set", "environmentNetworkPolicy.enabled=true",
		"--set", "environmentNetworkPolicy.allowClusterDNS=false")
	np := networkPolicy(t, docs, "environment-egress")

	assert.False(t, hasDNSRule(np), "allowClusterDNS=false must drop the cluster-DNS egress rule")
	require.Len(t, np.Spec.Egress, 1, "only the deny-list ipBlock rule should remain")
	require.NotNil(t, np.Spec.Egress[0].To[0].IPBlock)
	assert.Equal(t, "0.0.0.0/0", np.Spec.Egress[0].To[0].IPBlock.CIDR)
}

// TestEnvironmentNetworkPolicyExtraEgressAppends checks extraEgress entries
// are appended verbatim, letting an operator allow-list a destination the
// default deny-list rule would otherwise block (e.g. a specific RFC1918
// in-cluster service).
func TestEnvironmentNetworkPolicyExtraEgressAppends(t *testing.T) {
	docs := render(t,
		"--set", "environmentNetworkPolicy.enabled=true",
		"--set", "environmentNetworkPolicy.extraEgress[0].to[0].ipBlock.cidr=10.96.0.0/12")
	np := networkPolicy(t, docs, "environment-egress")

	found := false
	for _, rule := range np.Spec.Egress {
		for _, to := range rule.To {
			if to.IPBlock != nil && to.IPBlock.CIDR == "10.96.0.0/12" {
				found = true
			}
		}
	}
	assert.True(t, found, "extraEgress entry must be appended to the rendered policy")
}

// findIPBlockPeer returns the first egress `to` peer that is an ipBlock with
// the given CIDR, failing the test when none matches.
func findIPBlockPeer(t *testing.T, np *networkingv1.NetworkPolicy, cidr string) networkingv1.NetworkPolicyPeer {
	t.Helper()
	for _, rule := range np.Spec.Egress {
		for _, to := range rule.To {
			if to.IPBlock != nil && to.IPBlock.CIDR == cidr {
				return to
			}
		}
	}
	require.Fail(t, "no egress ipBlock peer found", "expected an ipBlock rule with cidr %q", cidr)
	return networkingv1.NetworkPolicyPeer{}
}

// hasDNSRule reports whether the policy admits egress to a kube-dns pod on
// port 53.
func hasDNSRule(np *networkingv1.NetworkPolicy) bool {
	for _, rule := range np.Spec.Egress {
		for _, to := range rule.To {
			if to.PodSelector != nil && to.PodSelector.MatchLabels["k8s-app"] == "kube-dns" {
				return true
			}
		}
		for _, p := range rule.Ports {
			if p.Port != nil && p.Port.IntVal == 53 {
				return true
			}
		}
	}
	return false
}
