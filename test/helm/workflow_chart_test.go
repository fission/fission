// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0
package helm

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/svcinfo"
)

// TestWorkflowChart is the drift check for the RFC-0022 workflow head: port
// constants against svcinfo, statestore env wiring, and — the known
// silent-drop bite — membership in BOTH NetworkPolicy allowlists (router
// internal listener + statestore).
func TestWorkflowChart(t *testing.T) {
	docs := render(t,
		"--set", "workflows.enabled=true",
		"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
		"--set", "networkPolicy.enabled=true",
		"--set", "serviceMonitor.enabled=true")

	t.Run("workflow deployment arg and env", func(t *testing.T) {
		deploy := find(docs, "Deployment", svcinfo.SvcWorkflow)
		args := containerArgs(t, deploy)
		assert.Equal(t, fmt.Sprint(svcinfo.PortWorkflow), argAfter(args, "--workflowPort"))

		env := containerEnv(t, deploy)
		assert.Equal(t, "client", env["STATESTORE_DRIVER"])
		assert.Contains(t, env["STATESTORE_DSN"], svcinfo.SvcStatestore)
		assert.Equal(t, svcinfo.RouterInternalURL("fission"), env["ROUTER_INTERNAL_URL"])
	})

	t.Run("workflow service", func(t *testing.T) {
		svc := servicePorts(t, find(docs, "Service", svcinfo.SvcWorkflow))
		require.Len(t, svc, 1)
		assert.EqualValues(t, svcinfo.PortWorkflow, svc[0]["port"])
		assert.EqualValues(t, svcinfo.PortWorkflow, svc[0]["targetPort"])
	})

	t.Run("networkpolicy allowlists admit svc:workflow", func(t *testing.T) {
		routerNP := find(docs, "NetworkPolicy", "router-allow-ingress")
		require.NotNil(t, routerNP)
		assert.True(t, npAllowsFromSvc(routerNP, svcinfo.SvcWorkflow),
			"the engine invokes on the internal listener; a missing row is a silent i/o timeout in CI")

		ssNP := find(docs, "NetworkPolicy", "statestore")
		require.NotNil(t, ssNP)
		assert.True(t, npAllowsFromSvc(ssNP, svcinfo.SvcWorkflow),
			"the engine reads/writes its log on the statestore")
	})

	t.Run("metrics are scrapable", func(t *testing.T) {
		// A ServiceMonitor without the pod exposing 8080 (or vice versa) is
		// silently-unscraped metrics — pin both halves.
		sm := find(docs, "ServiceMonitor", "workflow-monitor")
		require.NotNil(t, sm, "workflow ServiceMonitor must render when serviceMonitor.enabled")

		deploy := find(docs, "Deployment", svcinfo.SvcWorkflow)
		ports := containerPorts(t, deploy)
		assert.Contains(t, ports, 8080, "metrics containerPort")
	})

	t.Run("disabled by default", func(t *testing.T) {
		docs := render(t)
		assert.Nil(t, find(docs, "Deployment", svcinfo.SvcWorkflow))
	})
}
