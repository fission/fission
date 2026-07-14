// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestBundleDispatchTable pins the flag→service mapping and its precedence
// (first table match wins) — the contract the old startRequestedService
// early-return chain and the parallel getServiceNameFromArgs switch encoded
// twice. The webhook/canary/seed entries previously fell through to
// "Fission-Unknown" in the OTEL name switch; they now carry real names.
func TestBundleDispatchTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args CommandLineArgs
		want string
	}{
		{"seedTenants", CommandLineArgs{seedTenants: true}, "Fission-SeedTenants"},
		{"webhook", CommandLineArgs{webhookPort: 9443}, "Fission-Webhook"},
		{"canary", CommandLineArgs{canaryConfig: true}, "Fission-CanaryConfig"},
		{"router", CommandLineArgs{routerPort: 8888}, "Fission-Router"},
		{"executor", CommandLineArgs{executorPort: 8888}, "Fission-Executor"},
		{"kubewatcher", CommandLineArgs{kubewatcher: true}, "Fission-KubeWatcher"},
		{"timer", CommandLineArgs{timer: true}, "Fission-Timer"},
		{"mqt", CommandLineArgs{mqt: true}, "Fission-MessageQueueTrigger"},
		{"mqt_keda", CommandLineArgs{mqt_keda: true}, "Fission-Keda-MQTrigger"},
		{"mcp", CommandLineArgs{mcpPort: 8890}, "Fission-MCP"},
		{"tenantController", CommandLineArgs{tenantController: true}, "Fission-TenantController"},
		{"builderMgr", CommandLineArgs{builderMgr: true}, "Fission-BuilderMgr"},
		{"storagesvc", CommandLineArgs{storageServicePort: 8000}, "Fission-StorageSvc"},
		{"none selected", CommandLineArgs{}, "Fission-Unknown"},
		// Precedence: earlier table entries win when several flags are set
		// (matching the old early-return chain's order).
		{"router beats timer", CommandLineArgs{routerPort: 8888, timer: true}, "Fission-Router"},
		{"seed beats everything", CommandLineArgs{seedTenants: true, routerPort: 8888}, "Fission-SeedTenants"},
		{"webhook beats router", CommandLineArgs{webhookPort: 9443, routerPort: 8888}, "Fission-Webhook"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := tc.args
			assert.Equal(t, tc.want, getServiceNameFromArgs(&args))
		})
	}
}

// TestBundleDispatchTableComplete guards extensibility: every table entry has
// a name, a predicate, and a runner, and names are unique.
func TestBundleDispatchTableComplete(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, svc := range bundleServices() {
		assert.NotEmpty(t, svc.name)
		assert.NotNil(t, svc.selected, svc.name)
		assert.NotNil(t, svc.run, svc.name)
		assert.Falsef(t, seen[svc.name], "duplicate service name %s", svc.name)
		seen[svc.name] = true
	}
	assert.Len(t, seen, 14)
}
