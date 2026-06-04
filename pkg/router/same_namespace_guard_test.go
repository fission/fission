// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// mapLookup is a static callerNamespaceLookup for tests: ip -> namespace.
type mapLookup map[string]string

func (m mapLookup) lookup(ip string) (string, bool) {
	ns, ok := m[ip]
	return ns, ok
}

func TestSameNamespaceGuard(t *testing.T) {
	const installNS = "fission"

	cases := []struct {
		name       string
		callerIP   string
		lookup     mapLookup
		targetNS   string
		wantStatus int
		wantInner  bool
	}{
		{"same namespace allowed", "10.0.0.1:5000", mapLookup{"10.0.0.1": "tenant-a"}, "tenant-a", http.StatusOK, true},
		{"internal component (install ns) may invoke any namespace", "10.0.0.2:5000", mapLookup{"10.0.0.2": installNS}, "tenant-b", http.StatusOK, true},
		{"cross-namespace forbidden", "10.0.0.3:5000", mapLookup{"10.0.0.3": "tenant-a"}, "tenant-b", http.StatusForbidden, false},
		{"unresolved caller IP forbidden (fail closed)", "10.0.0.9:5000", mapLookup{}, "tenant-a", http.StatusForbidden, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			innerCalled := false
			inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				innerCalled = true
				w.WriteHeader(http.StatusOK)
			})
			g := &sameNamespaceGuard{lookup: tc.lookup, installNamespace: installNS, logger: loggerfactory.GetLogger()}
			h := g.wrap(inner, tc.targetNS)

			req := httptest.NewRequest(http.MethodPost, "/fission-function/"+tc.targetNS+"/fn", nil)
			req.RemoteAddr = tc.callerIP
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, tc.wantInner, innerCalled, "inner handler reached?")
		})
	}
}

func TestClientIP(t *testing.T) {
	assert.Equal(t, "10.0.0.1", clientIP("10.0.0.1:5678"))
	assert.Equal(t, "10.0.0.1", clientIP("10.0.0.1"))     // no port
	assert.Equal(t, "fe80::1", clientIP("[fe80::1]:443")) // ipv6
}

func TestPodIPCache(t *testing.T) {
	c := &podIPCache{ipToPod: map[string]podRef{}}

	c.set("10.0.0.1", podRef{namespace: "ns1", name: "pod-a"})
	ns, ok := c.lookup("10.0.0.1")
	assert.True(t, ok)
	assert.Equal(t, "ns1", ns)

	_, ok = c.lookup("10.0.0.99")
	assert.False(t, ok, "unknown IP must not resolve")

	// IP recycling: pod-b takes over 10.0.0.1 in ns2; a late delete for pod-a must
	// NOT evict pod-b's mapping.
	c.set("10.0.0.1", podRef{namespace: "ns2", name: "pod-b"})
	c.del("10.0.0.1", "pod-a")
	ns, ok = c.lookup("10.0.0.1")
	assert.True(t, ok)
	assert.Equal(t, "ns2", ns, "stale delete for the previous owner must not evict the recycled IP")

	// The matching delete removes it.
	c.del("10.0.0.1", "pod-b")
	_, ok = c.lookup("10.0.0.1")
	assert.False(t, ok)

	// Empty IP is ignored on both set and lookup.
	c.set("", podRef{namespace: "x", name: "y"})
	_, ok = c.lookup("")
	assert.False(t, ok)
}
