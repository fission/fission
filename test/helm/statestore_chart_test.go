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

// TestStatestoreChartPorts is the drift check for the embedded statestore head:
// its --statestorePort arg and Service port must equal svcinfo.PortStatestore
// (RFC-0021). It renders with the store enabled, since it is off by default.
func TestStatestoreChartPorts(t *testing.T) {
	docs := render(t, "--set", "statestore.enabled=true", "--set", "statestore.mode=embedded")

	t.Run("statestore deployment arg", func(t *testing.T) {
		args := containerArgs(t, find(docs, "Deployment", svcinfo.SvcStatestore))
		assert.Equal(t, fmt.Sprint(svcinfo.PortStatestore), argAfter(args, "--statestorePort"))
	})

	t.Run("statestore service", func(t *testing.T) {
		svc := servicePorts(t, find(docs, "Service", svcinfo.SvcStatestore))
		require.Len(t, svc, 1)
		assert.EqualValues(t, svcinfo.PortStatestore, svc[0]["port"])
		assert.EqualValues(t, svcinfo.PortStatestore, svc[0]["targetPort"])
	})
}

// TestStateSvcChart is the drift check for the RFC-0023 statesvc head: port
// constants against svcinfo, statestore client-driver env wiring, and
// membership in the statestore NetworkPolicy allowlist (the silent-drop bite).
func TestStateSvcChart(t *testing.T) {
	docs := render(t,
		"--set", "functionState.enabled=true",
		"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
		"--set", "networkPolicy.enabled=true")

	t.Run("statesvc deployment arg and env", func(t *testing.T) {
		deploy := find(docs, "Deployment", svcinfo.SvcStateSvc)
		args := containerArgs(t, deploy)
		assert.Equal(t, fmt.Sprint(svcinfo.PortStateSvc), argAfter(args, "--stateApiPort"))

		env := containerEnv(t, deploy)
		assert.Equal(t, "client", env["STATESTORE_DRIVER"])
		assert.Contains(t, env["STATESTORE_DSN"], svcinfo.SvcStatestore)
	})

	t.Run("statesvc service", func(t *testing.T) {
		svc := servicePorts(t, find(docs, "Service", svcinfo.SvcStateSvc))
		require.Len(t, svc, 1)
		assert.EqualValues(t, svcinfo.PortStateSvc, svc[0]["port"])
		assert.EqualValues(t, svcinfo.PortStateSvc, svc[0]["targetPort"])
	})

	t.Run("statestore networkpolicy admits svc:statesvc", func(t *testing.T) {
		ssNP := find(docs, "NetworkPolicy", "statestore")
		require.NotNil(t, ssNP)
		assert.True(t, npAllowsFromSvc(ssNP, svcinfo.SvcStateSvc),
			"statesvc reads/writes keyspaces on the statestore; a missing row is a silent i/o timeout in CI")
	})

	t.Run("statesvc networkpolicy renders", func(t *testing.T) {
		require.NotNil(t, find(docs, "NetworkPolicy", svcinfo.SvcStateSvc),
			"function pods reach statesvc across namespaces; the policy must render with the head")
	})

	t.Run("metrics are scrapable", func(t *testing.T) {
		// A ServiceMonitor without the pod exposing 8080 (or vice versa) is
		// silently-unscraped metrics — statesvc emits fission_statestore_*
		// through the OTel→Prometheus bridge, so pin both halves.
		mdocs := render(t,
			"--set", "functionState.enabled=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
			"--set", "serviceMonitor.enabled=true")
		require.NotNil(t, find(mdocs, "ServiceMonitor", "statesvc-monitor"),
			"statesvc ServiceMonitor must render when serviceMonitor.enabled")
		deploy := find(mdocs, "Deployment", svcinfo.SvcStateSvc)
		assert.Contains(t, containerPorts(t, deploy), 8080, "metrics containerPort")
	})

	t.Run("renders with coverage enabled (CI profile)", func(t *testing.T) {
		// The kind-ci profile sets coverage.enabled=true; a stray coverage
		// volumeMount without its own volumeMounts key lands under ports and
		// fails the server-side apply (containerPort=0 + mountPath). Render the
		// exact CI shape and assert no port carries a mountPath.
		docs := render(t,
			"--set", "functionState.enabled=true",
			"--set", "statestore.enabled=true", "--set", "statestore.mode=embedded",
			"--set", "coverage.enabled=true")
		deploy := find(docs, "Deployment", svcinfo.SvcStateSvc)
		require.NotNil(t, deploy)
		containers := deploy["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
		ports, _ := containers[0].(map[string]any)["ports"].([]any)
		for _, p := range ports {
			_, hasMount := p.(map[string]any)["mountPath"]
			assert.False(t, hasMount, "a port carries a mountPath — coverage volumeMount leaked into ports")
		}
	})

	t.Run("disabled by default", func(t *testing.T) {
		docs := render(t)
		assert.Nil(t, find(docs, "Deployment", svcinfo.SvcStateSvc))
	})
}
