// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package helm holds chart↔code drift checks: the Helm chart mirrors the
// port/service-name constants in pkg/svcinfo, and nothing but these tests
// ties the two together. They exec `helm template` (skipping when helm is
// not installed — CI runners have it) and compare the rendered manifests
// against the constants.
package helm

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/svcinfo"
)

// TestChartPortsMatchSvcinfo is the drift check: every port/URL surface the
// chart renders must equal the pkg/svcinfo constants. If this test fails you
// changed one side without the other — Services, NetworkPolicies, and probes
// come from the chart, while Go code dials the constants.
func TestChartPortsMatchSvcinfo(t *testing.T) {
	docs := render(t, "--set", "mcp.enabled=true", "--set", "mcp.allowInsecure=true")

	t.Run("router deployment args and container ports", func(t *testing.T) {
		router := deployment(t, docs, svcinfo.SvcRouter)
		args := firstContainer(t, router).Args
		assert.Equal(t, fmt.Sprint(svcinfo.PortRouter), argAfter(args, "--routerPort"))
		assert.Equal(t, fmt.Sprint(svcinfo.PortRouterInternal), argAfter(args, "--routerInternalPort"))

		got := containerPorts(t, router)
		assert.Contains(t, got, int32(svcinfo.PortRouter))
		assert.Contains(t, got, int32(svcinfo.PortRouterInternal))
	})

	t.Run("router services", func(t *testing.T) {
		public := servicePorts(t, docs, svcinfo.SvcRouter)
		require.Len(t, public, 1)
		assert.EqualValues(t, svcinfo.PortRouter, public[0].TargetPort.IntValue())

		internal := servicePorts(t, docs, svcinfo.SvcRouterInternal)
		require.Len(t, internal, 1)
		assert.EqualValues(t, svcinfo.PortRouterInternal, internal[0].Port)
		assert.EqualValues(t, svcinfo.PortRouterInternal, internal[0].TargetPort.IntValue())
	})

	t.Run("executor and storagesvc services", func(t *testing.T) {
		executor := servicePorts(t, docs, svcinfo.SvcExecutor)
		require.Len(t, executor, 1)
		assert.EqualValues(t, svcinfo.PortExecutor, executor[0].TargetPort.IntValue())

		storage := servicePorts(t, docs, svcinfo.SvcStorage)
		require.Len(t, storage, 1)
		assert.EqualValues(t, svcinfo.PortStorageSvc, storage[0].TargetPort.IntValue())
	})

	t.Run("mcp deployment arg", func(t *testing.T) {
		args := containerArgs(t, docs, "mcp")
		assert.Equal(t, fmt.Sprint(svcinfo.PortMCP), argAfter(args, "--mcpPort"))
	})

	t.Run("mcp service", func(t *testing.T) {
		mcp := servicePorts(t, docs, svcinfo.SvcMCP)
		require.Len(t, mcp, 1)
		assert.EqualValues(t, svcinfo.PortMCP, mcp[0].Port)
		assert.EqualValues(t, svcinfo.PortMCP, mcp[0].TargetPort.IntValue())
	})

	t.Run("ROUTER_INTERNAL_URL on internal publishers", func(t *testing.T) {
		want := svcinfo.RouterInternalURL("fission")
		for _, name := range []string{"kubewatcher", "timer", "mqtrigger-keda", "mcp"} {
			doc := deployment(t, docs, name)
			assert.Equalf(t, want, containerEnv(t, doc)["ROUTER_INTERNAL_URL"],
				"deployment %s ROUTER_INTERNAL_URL", name)
		}
	})

	t.Run("sibling-service URL args", func(t *testing.T) {
		router := containerArgs(t, docs, svcinfo.SvcRouter)
		// The chart renders the ABSOLUTE (trailing-dot) form of the svcinfo
		// URL — same service, namespace, and implicit port, plus the
		// .svc.<clusterDomain>. suffix that keeps the router's cold-start
		// dials off the resolv.conf search path. In-process consumers (the
		// e2e/portless framework) deliberately keep the short svcinfo form:
		// they resolve by registry name, not DNS.
		assert.Equal(t, svcinfo.ExecutorURL("fission")+".svc.cluster.local.", argAfter(router, "--executorUrl"))

		buildermgr := containerArgs(t, docs, "buildermgr")
		assert.Equal(t, svcinfo.StorageSvcURL("fission"), argAfter(buildermgr, "--storageSvcUrl"))
	})

	t.Run("webhook deployment port", func(t *testing.T) {
		args := containerArgs(t, docs, "webhook")
		assert.Equal(t, fmt.Sprint(svcinfo.PortWebhook), argAfter(args, "--webhookPort"))
	})
}
