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
		deploy := deployment(t, docs, svcinfo.SvcWorkflow)
		args := firstContainer(t, deploy).Args
		assert.Equal(t, fmt.Sprint(svcinfo.PortWorkflow), argAfter(args, "--workflowPort"))

		env := containerEnv(t, deploy)
		assert.Equal(t, "client", env["STATESTORE_DRIVER"])
		assert.Contains(t, env["STATESTORE_DSN"], svcinfo.SvcStatestore)
		assert.Equal(t, svcinfo.RouterInternalURL("fission"), env["ROUTER_INTERNAL_URL"])
	})

	t.Run("workflow service", func(t *testing.T) {
		svc := servicePorts(t, docs, svcinfo.SvcWorkflow)
		require.Len(t, svc, 1)
		assert.EqualValues(t, svcinfo.PortWorkflow, svc[0].Port)
		assert.EqualValues(t, svcinfo.PortWorkflow, svc[0].TargetPort.IntValue())
	})

	t.Run("networkpolicy allowlists admit svc:workflow", func(t *testing.T) {
		routerNP := networkPolicy(t, docs, "router-allow-ingress")
		assert.True(t, npAllowsFromSvc(routerNP, svcinfo.SvcWorkflow),
			"the engine invokes on the internal listener; a missing row is a silent i/o timeout in CI")

		ssNP := networkPolicy(t, docs, "statestore")
		assert.True(t, npAllowsFromSvc(ssNP, svcinfo.SvcWorkflow),
			"the engine reads/writes its log on the statestore")
	})

	t.Run("metrics are scrapable", func(t *testing.T) {
		// A ServiceMonitor without the pod exposing 8080 (or vice versa) is
		// silently-unscraped metrics — pin both halves.
		sm := docs.find("ServiceMonitor", "workflow-monitor")
		require.NotNil(t, sm, "workflow ServiceMonitor must render when serviceMonitor.enabled")

		deploy := deployment(t, docs, svcinfo.SvcWorkflow)
		ports := containerPorts(t, deploy)
		assert.Contains(t, ports, int32(8080), "metrics containerPort")
	})

	t.Run("disabled by default", func(t *testing.T) {
		docs := render(t)
		assert.Nil(t, docs.find("Deployment", svcinfo.SvcWorkflow))
	})
}
