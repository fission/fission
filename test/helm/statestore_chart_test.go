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
		args := containerArgs(t, docs, svcinfo.SvcStatestore)
		assert.Equal(t, fmt.Sprint(svcinfo.PortStatestore), argAfter(args, "--statestorePort"))
	})

	t.Run("statestore service", func(t *testing.T) {
		svc := servicePorts(t, docs, svcinfo.SvcStatestore)
		require.Len(t, svc, 1)
		assert.EqualValues(t, svcinfo.PortStatestore, svc[0].Port)
		assert.EqualValues(t, svcinfo.PortStatestore, svc[0].TargetPort.IntValue())
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
		deploy := deployment(t, docs, svcinfo.SvcStateSvc)
		args := firstContainer(t, deploy).Args
		assert.Equal(t, fmt.Sprint(svcinfo.PortStateSvc), argAfter(args, "--stateApiPort"))

		env := containerEnv(t, deploy)
		assert.Equal(t, "client", env["STATESTORE_DRIVER"])
		assert.Contains(t, env["STATESTORE_DSN"], svcinfo.SvcStatestore)
	})

	t.Run("statesvc service", func(t *testing.T) {
		svc := servicePorts(t, docs, svcinfo.SvcStateSvc)
		require.Len(t, svc, 1)
		assert.EqualValues(t, svcinfo.PortStateSvc, svc[0].Port)
		assert.EqualValues(t, svcinfo.PortStateSvc, svc[0].TargetPort.IntValue())
	})

	t.Run("statestore networkpolicy admits svc:statesvc", func(t *testing.T) {
		ssNP := networkPolicy(t, docs, "statestore")
		assert.True(t, npAllowsFromSvc(ssNP, svcinfo.SvcStateSvc),
			"statesvc reads/writes keyspaces on the statestore; a missing row is a silent i/o timeout in CI")
	})

	t.Run("statesvc networkpolicy renders", func(t *testing.T) {
		require.NotNil(t, docs.find("NetworkPolicy", svcinfo.SvcStateSvc),
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
		require.NotNil(t, mdocs.find("ServiceMonitor", "statesvc-monitor"),
			"statesvc ServiceMonitor must render when serviceMonitor.enabled")
		deploy := deployment(t, mdocs, svcinfo.SvcStateSvc)
		assert.Contains(t, containerPorts(t, deploy), int32(8080), "metrics containerPort")
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
		// A leaked volumeMount under ports decodes as a ContainerPort with no
		// containerPort (0) and no name; the typed decode drops the unknown
		// mountPath key, so assert on what would remain of the stray entry.
		deploy := deployment(t, docs, svcinfo.SvcStateSvc)
		for _, p := range firstContainer(t, deploy).Ports {
			assert.NotZero(t, p.ContainerPort, "a port with no containerPort — coverage volumeMount leaked into ports")
		}
	})

	t.Run("disabled by default", func(t *testing.T) {
		docs := render(t)
		assert.Nil(t, docs.find("Deployment", svcinfo.SvcStateSvc))
	})
}
