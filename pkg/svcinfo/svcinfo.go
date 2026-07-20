// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package svcinfo is the single source of truth for the ports and service
// names of Fission's control-plane components. Every Go-side reference to a
// component port or in-cluster service name must come from here; the Helm
// chart mirrors these values in values.yaml; the drift check in test/helm
// (TestChartPortsMatchSvcinfo) asserts the rendered chart matches these
// constants, and TestPortValues pins the raw numbers for environments where
// that check skips (no helm binary).
//
// Changing a port here is NOT enough to change it in a cluster: Services,
// NetworkPolicies, and probes render from the chart. These constants exist so
// Go code agrees with itself and so the drift check compares one anchor
// against the rendered chart.
package svcinfo

import "fmt"

// Component ports.
//
// PortExecutor and PortEnvRuntime are both 8888 by coincidence, not by
// contract: the executor API serves on the executor pod, while the
// env-runtime port is the container port every function pod's runtime
// listens on (what the router proxies to). Never merge them.
const (
	// PortRouter is the router's public listener (user HTTPTriggers,
	// /router-healthz, /_version). Fronted by Service port 80 in the chart.
	PortRouter = 8888
	// PortRouterInternal is the router listener hosting
	// /fission-function/<ns>/<name> after the GHSA-3g33-6vg6-27m8 split.
	// Must match the chart's router-internal Service targetPort.
	PortRouterInternal = 8889
	// PortExecutor is the executor's API port (tapService/specialize RPC).
	PortExecutor = 8888
	// PortEnvRuntime is the container port a function pod's environment
	// runtime listens on; the router and fetcher dial it.
	PortEnvRuntime = 8888
	// PortFetcher is the fetcher sidecar's port in function and builder pods.
	PortFetcher = 8000
	// PortBuilder is the builder container's port in builder pods.
	PortBuilder = 8001
	// PortStorageSvc is the storage service's API port.
	PortStorageSvc = 8000
	// PortMCP is the MCP tool server's port (RFC-0011).
	PortMCP = 8890
	// PortStatestore is the embedded statestore's capability API port (RFC-0021).
	PortStatestore = 8891
	// PortWorkflow is the workflow engine head's port (RFC-0022): health
	// probes plus the read-only run-history endpoint.
	PortWorkflow = 8892
	// PortStateSvc is the statesvc function-facing keyed-state API port
	// (RFC-0023). Scoped surface only — the raw statestore stays on
	// PortStatestore, unreachable from function pods.
	PortStateSvc = 8893
	// PortMetrics is the default Prometheus metrics port every component
	// serves when METRICS_ADDR is unset; chart ServiceMonitors scrape it.
	PortMetrics = 8080
	// PortHealthProbe is the default health-probe port for Manager-based
	// components that bind one separately (buildermgr).
	PortHealthProbe = 8081
	// PortWebhook is the admission webhook's TLS port.
	PortWebhook = 9443
)

// In-cluster Service names of the control-plane components (in the release
// namespace). The CLI healthcheck and URL helpers key off these.
const (
	SvcRouter         = "router"
	SvcRouterInternal = "router-internal"
	SvcExecutor       = "executor"
	SvcStorage        = "storagesvc"
	SvcWebhook        = "webhook-service"
	SvcMCP            = "mcp"
	SvcStatestore     = "statestore"
	SvcWorkflow       = "workflow"
)

// RouterURL returns the in-cluster URL of the router's public listener
// (Service port 80, so no explicit port).
func RouterURL(namespace string) string {
	return fmt.Sprintf("http://%s.%s", SvcRouter, namespace)
}

// RouterInternalURL returns the in-cluster URL of the router's internal
// listener (ClusterIP-only Service, port = PortRouterInternal).
func RouterInternalURL(namespace string) string {
	return fmt.Sprintf("http://%s.%s:%d", SvcRouterInternal, namespace, PortRouterInternal)
}

// ExecutorURL returns the in-cluster URL of the executor API (Service port
// 80 in the chart, so no explicit port).
func ExecutorURL(namespace string) string {
	return fmt.Sprintf("http://%s.%s", SvcExecutor, namespace)
}

// StorageSvcURL returns the in-cluster URL of the storage service (Service
// port 80 in the chart, so no explicit port).
func StorageSvcURL(namespace string) string {
	return fmt.Sprintf("http://%s.%s", SvcStorage, namespace)
}
