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
)

// TestClusterDomainAbsoluteNames guards the DNS-hot-path contract: the
// in-cluster URLs the chart hands to the router, and the CLUSTER_DOMAIN env
// the executor/buildermgr use to build service FQDNs in code, must render as
// ABSOLUTE names (trailing dot / explicit domain). A regression back to the
// short "<name>.<namespace>" form re-enables the resolv.conf search-path
// walk whose extra queries drop under parallel load ("lookup <svc>: i/o
// timeout" cascades).
func TestClusterDomainAbsoluteNames(t *testing.T) {
	t.Parallel()

	containerOf := func(docs manifests, name string) corev1.Container {
		t.Helper()
		return firstContainer(t, deployment(t, docs, name))
	}

	t.Run("defaults render cluster.local absolutes", func(t *testing.T) {
		t.Parallel()
		docs := render(t)

		router := containerOf(docs, "router")
		joined := strings.Join(router.Args, " ")
		assert.Contains(t, joined, "http://executor.fission.svc.cluster.local.",
			"the router's executor URL must be an absolute FQDN with the trailing dot")

		for _, dep := range []string{"executor", "buildermgr"} {
			v, ok := envValue(containerOf(docs, dep), "CLUSTER_DOMAIN")
			require.Truef(t, ok, "%s must carry CLUSTER_DOMAIN for utils.ServiceFQDN", dep)
			assert.Equal(t, "cluster.local", v, dep)
		}
	})

	t.Run("clusterDomain override reaches every consumer", func(t *testing.T) {
		t.Parallel()
		docs := render(t, "--set", "clusterDomain=example.org")

		router := containerOf(docs, "router")
		joined := strings.Join(router.Args, " ")
		assert.Contains(t, joined, "http://executor.fission.svc.example.org.",
			"the override must reach the router's executor URL")

		for _, dep := range []string{"executor", "buildermgr"} {
			v, _ := envValue(containerOf(docs, dep), "CLUSTER_DOMAIN")
			assert.Equalf(t, "example.org", v, "%s must receive the overridden domain", dep)
		}
	})
}
