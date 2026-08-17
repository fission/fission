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
	rbacv1 "k8s.io/api/rbac/v1"
)

// TestBuildermgrCanRollBuilderDeployments guards an RBAC gap that fails CLOSED
// and silently: buildermgr rolls a builder Deployment whose pod template has
// drifted from the chart (RFC-0028 phase 3, #776), which needs `update` on
// deployments. The Role historically granted only list/create/delete.
//
// Nothing else would catch this. The reconciler's unit tests use a fake
// clientset, which does not enforce RBAC, so they pass either way; in a real
// cluster the drift is detected, the update is denied, and the builder keeps
// running the stale image — the exact symptom the feature exists to remove.
func TestBuildermgrCanRollBuilderDeployments(t *testing.T) {
	docs := render(t)

	var found bool
	for _, role := range roles(t, docs) {
		if !strings.Contains(role.Name, "buildermgr") {
			continue
		}
		verbs := verbsFor(role.Rules, "apps", "deployments")
		if len(verbs) == 0 {
			continue
		}
		found = true
		for _, want := range []string{"list", "create", "update", "delete"} {
			assert.Truef(t, verbs[want],
				"%s %q must grant %q on apps/deployments (have %v)", role.Kind, role.Name, want, verbs)
		}
	}
	require.True(t, found, "no buildermgr Role/ClusterRole granting apps/deployments was rendered")
}

// verbsFor returns the verbs a rendered Role/ClusterRole grants for a resource
// in an API group, merged across every rule that mentions it.
func verbsFor(rules []rbacv1.PolicyRule, apiGroup, resource string) map[string]bool {
	out := map[string]bool{}
	for _, rule := range rules {
		if !slices.Contains(rule.APIGroups, apiGroup) || !slices.Contains(rule.Resources, resource) {
			continue
		}
		for _, v := range rule.Verbs {
			if v != "" {
				out[v] = true
			}
		}
	}
	return out
}
