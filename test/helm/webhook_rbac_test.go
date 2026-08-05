// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0
package helm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWebhookEnvReaderRBACScopesByTenancy pins the RFC-0023 webhook
// environment-read RBAC to the same tenancy posture as every other Fission-CRD
// read (review feedback on #3593): a namespaced Role per Fission namespace in
// static mode (a cluster-wide grant would be over-privileged), and a
// cluster-wide ClusterRole added only in dynamic/cluster mode.
func TestWebhookEnvReaderRBACScopesByTenancy(t *testing.T) {
	const roleName = "fission-webhook-env-reader" // render() uses release name "fission"

	t.Run("static: namespaced Roles, no ClusterRole", func(t *testing.T) {
		docs := render(t, "--set", "additionalFissionNamespaces={fission-fn}")
		assert.GreaterOrEqual(t, countByKindName(docs, "Role", roleName), 2,
			"a Role in defaultNamespace + each additionalFissionNamespaces")
		assert.Zero(t, countByKindName(docs, "ClusterRole", roleName),
			"static tenancy must not grant cluster-wide environment read")
	})

	t.Run("dynamic: adds the ClusterRole", func(t *testing.T) {
		docs := render(t, "--set", "tenancy.mode=dynamic")
		assert.Equal(t, 1, countByKindName(docs, "ClusterRole", roleName),
			"dynamic tenancy onboards namespaces at runtime, so the read is cluster-wide")
	})
}
