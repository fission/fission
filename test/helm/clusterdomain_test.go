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

// TestClusterDomainAbsoluteNames guards the DNS-hot-path contract: the
// in-cluster URLs the chart hands to the router, and the CLUSTER_DOMAIN env
// the executor/buildermgr use to build service FQDNs in code, must render as
// ABSOLUTE names (trailing dot / explicit domain). A regression back to the
// short "<name>.<namespace>" form re-enables the resolv.conf search-path
// walk whose extra queries drop under parallel load ("lookup <svc>: i/o
// timeout" cascades).
func TestClusterDomainAbsoluteNames(t *testing.T) {
	t.Parallel()

	containerOf := func(docs []map[string]any, kind, name string) map[string]any {
		t.Helper()
		for _, d := range docs {
			if d["kind"] != kind {
				continue
			}
			meta := d["metadata"].(map[string]any)
			if meta["name"] != name {
				continue
			}
			spec := d["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
			return spec["containers"].([]any)[0].(map[string]any)
		}
		t.Fatalf("%s %q not rendered", kind, name)
		return nil
	}

	envValue := func(c map[string]any, key string) (string, bool) {
		t.Helper()
		for _, e := range c["env"].([]any) {
			ev := e.(map[string]any)
			if ev["name"] == key {
				v, _ := ev["value"].(string)
				return v, true
			}
		}
		return "", false
	}

	t.Run("defaults render cluster.local absolutes", func(t *testing.T) {
		t.Parallel()
		docs := render(t)

		router := containerOf(docs, "Deployment", "router")
		joined := strings.Join(func() []string {
			var out []string
			for _, a := range router["args"].([]any) {
				out = append(out, a.(string))
			}
			return out
		}(), " ")
		assert.Contains(t, joined, "http://executor.fission.svc.cluster.local.",
			"the router's executor URL must be an absolute FQDN with the trailing dot")

		for _, dep := range []string{"executor", "buildermgr"} {
			v, ok := envValue(containerOf(docs, "Deployment", dep), "CLUSTER_DOMAIN")
			require.Truef(t, ok, "%s must carry CLUSTER_DOMAIN for utils.ServiceFQDN", dep)
			assert.Equal(t, "cluster.local", v, dep)
		}
	})

	t.Run("clusterDomain override reaches every consumer", func(t *testing.T) {
		t.Parallel()
		docs := render(t, "--set", "clusterDomain=example.org")

		router := containerOf(docs, "Deployment", "router")
		var joined strings.Builder
		for _, a := range router["args"].([]any) {
			joined.WriteString(a.(string) + " ")
		}
		assert.Contains(t, joined.String(), "http://executor.fission.svc.example.org.",
			"the override must reach the router's executor URL")

		for _, dep := range []string{"executor", "buildermgr"} {
			v, _ := envValue(containerOf(docs, "Deployment", dep), "CLUSTER_DOMAIN")
			assert.Equalf(t, "example.org", v, "%s must receive the overridden domain", dep)
		}
	})
}
