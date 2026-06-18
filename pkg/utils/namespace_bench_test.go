// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"fmt"
	"testing"
)

// resolverWithTenants builds a NamespaceResolver holding n tenant namespaces.
func resolverWithTenants(n int) *NamespaceResolver {
	tenants := make(map[string]string, n)
	for i := 0; i < n; i++ {
		ns := fmt.Sprintf("team-%03d", i)
		tenants[ns] = ns
	}
	r := &NamespaceResolver{}
	r.SetTenants(tenants)
	return r
}

// BenchmarkNamespaceResolverIsTenant measures the membership check the multi-
// namespace tenancy work put on the hot paths (the watch-membership predicate on
// every CRD event, the per-cold-start specialization namespace gate). It is an
// RLock + a single map lookup and returns no copy, so it should be a handful of
// nanoseconds and allocation-free regardless of tenant count — the property that
// keeps dynamic tenancy off the request-latency budget.
func BenchmarkNamespaceResolverIsTenant(b *testing.B) {
	r := resolverWithTenants(50)
	b.ResetTimer()
	for b.Loop() {
		_ = r.IsTenant("team-025")
	}
}

// BenchmarkNamespaceResolverFissionResourceNamespaces measures the copy-returning
// getter, for contrast: it allocates and copies the whole tenant map under RLock,
// so it scales with tenant count. It is deliberately confined to startup / periodic
// reaper / informer-build loops (never a per-request path) — this benchmark exists
// to document why the hot paths use IsTenant (above) instead.
func BenchmarkNamespaceResolverFissionResourceNamespaces(b *testing.B) {
	r := resolverWithTenants(50)
	b.ResetTimer()
	for b.Loop() {
		_ = r.FissionResourceNamespaces()
	}
}
