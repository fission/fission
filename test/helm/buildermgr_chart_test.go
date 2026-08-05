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
	for _, doc := range docs {
		kind, _ := doc["kind"].(string)
		if kind != "Role" && kind != "ClusterRole" {
			continue
		}
		meta, _ := doc["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		if !strings.Contains(name, "buildermgr") {
			continue
		}
		verbs := verbsFor(doc, "apps", "deployments")
		if len(verbs) == 0 {
			continue
		}
		found = true
		for _, want := range []string{"list", "create", "update", "delete"} {
			assert.Truef(t, verbs[want],
				"%s %q must grant %q on apps/deployments (have %v)", kind, name, want, verbs)
		}
	}
	require.True(t, found, "no buildermgr Role/ClusterRole granting apps/deployments was rendered")
}
