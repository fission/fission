// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamespaceResolver(t *testing.T) {
	t.Run("GetBuilderNS", func(t *testing.T) {
		for _, test := range []struct {
			name              string
			namespaceResolver *NamespaceResolver
			namespace         string
			expected          string
		}{
			{
				name:              "should return fission-builder namespace",
				namespaceResolver: getFissionNamespaces("fission-builder", "fission-function", "default"),
				namespace:         "default",
				expected:          "fission-builder",
			},
			{
				name:              "should return testns2 namespace",
				namespaceResolver: getFissionNamespaces("fission-builder", "fission-function", "default"),
				namespace:         "testns2",
				expected:          "testns2",
			},
			{
				name:              "should return fission-builder namespace",
				namespaceResolver: getFissionNamespaces("fission-builder", "", "default"),
				namespace:         "default",
				expected:          "fission-builder",
			},
			{
				name:              "should return testns3 namespace",
				namespaceResolver: getFissionNamespaces("fission-builder", "", "testns"),
				namespace:         "testns3",
				expected:          "testns3",
			},
			{
				name:              "should return testns4 namespace",
				namespaceResolver: getFissionNamespaces("", "fission-function", "default"),
				namespace:         "testns4",
				expected:          "testns4",
			},
			{
				name:              "should return testns5 namespace",
				namespaceResolver: getFissionNamespaces("", "fission-function", "testns"),
				namespace:         "testns5",
				expected:          "testns5",
			},
			{
				name:              "should return default namespace",
				namespaceResolver: getFissionNamespaces("", "", "default"),
				namespace:         "default",
				expected:          "default",
			},
			{
				name:              "should return testns6 namespace",
				namespaceResolver: getFissionNamespaces("", "", "default"),
				namespace:         "testns6",
				expected:          "testns6",
			},
			{
				name:              "should return testns7 namespace",
				namespaceResolver: getFissionNamespaces("", "", ""),
				namespace:         "testns7",
				expected:          "testns7",
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				ns := test.namespaceResolver.GetBuilderNS(test.namespace)
				if ns != test.expected {
					t.Errorf("expected builder namespace %s, got %s", test.expected, ns)
				}
			})
		}
	})

	t.Run("GetFunctionNS", func(t *testing.T) {
		for _, test := range []struct {
			name              string
			namespaceResolver *NamespaceResolver
			namespace         string
			expected          string
		}{
			{
				name:              "should return fission-function namespace",
				namespaceResolver: getFissionNamespaces("fission-builder", "fission-function", "default"),
				namespace:         "default",
				expected:          "fission-function",
			},
			{
				name:              "should return testns2 namespace",
				namespaceResolver: getFissionNamespaces("fission-builder", "fission-function", "default"),
				namespace:         "testns2",
				expected:          "testns2",
			},
			{
				name:              "should return fission-function namespace",
				namespaceResolver: getFissionNamespaces("", "fission-function", "default"),
				namespace:         "default",
				expected:          "fission-function",
			},
			{
				name:              "should return testns3 namespace",
				namespaceResolver: getFissionNamespaces("", "fission-function", "testns"),
				namespace:         "testns3",
				expected:          "testns3",
			},
			{
				name:              "should return testns4 namespace",
				namespaceResolver: getFissionNamespaces("fission-builder", "", "default"),
				namespace:         "testns4",
				expected:          "testns4",
			},
			{
				name:              "should return testns5 namespace",
				namespaceResolver: getFissionNamespaces("fission-builder", "", "testns"),
				namespace:         "testns5",
				expected:          "testns5",
			},
			{
				name:              "should return default namespace",
				namespaceResolver: getFissionNamespaces("", "", "default"),
				namespace:         "default",
				expected:          "default",
			},
			{
				name:              "should return testns6 namespace",
				namespaceResolver: getFissionNamespaces("", "", "default"),
				namespace:         "testns6",
				expected:          "testns6",
			},
			{
				name:              "should return testns7 namespace",
				namespaceResolver: getFissionNamespaces("", "", ""),
				namespace:         "testns7",
				expected:          "testns7",
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				ns := test.namespaceResolver.GetFunctionNS(test.namespace)
				if ns != test.expected {
					t.Errorf("expected function namespace %s, got %s", test.expected, ns)
				}
			})
		}
	})

	t.Run("ResolveNamespace", func(t *testing.T) {
		for _, test := range []struct {
			name              string
			namespaceResolver *NamespaceResolver
			namespace         string
			expected          string
		}{
			{
				name:              "should return testns namespace",
				namespaceResolver: getFissionNamespaces("fission-builder", "fission-function", "default"),
				namespace:         "testns",
				expected:          "testns",
			},
			{
				name:              "should return default namespace",
				namespaceResolver: getFissionNamespaces("fission-builder", "", "default"),
				namespace:         "testns",
				expected:          "default",
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				ns := test.namespaceResolver.ResolveNamespace(test.namespace)
				require.Equal(t, test.expected, ns, "Resolved namespace mismatch")
			})
		}
	})

	t.Run("getNamespace", func(t *testing.T) {
		for _, test := range []struct {
			name                string
			defaultNamespace    string
			additionalNamespace string
			expected            int
		}{
			{
				name:                "length of namespaces should be 1",
				defaultNamespace:    "",
				additionalNamespace: "",
				expected:            1,
			},
			{
				name:                "length of namespaces should be 1",
				defaultNamespace:    "default",
				additionalNamespace: "",
				expected:            1,
			},
			{
				name:                "length of namespaces should be 1",
				defaultNamespace:    "default",
				additionalNamespace: "default",
				expected:            1,
			},
			{
				name:                "length of namespaces should be 3",
				defaultNamespace:    "default",
				additionalNamespace: "testns1,testns2",
				expected:            3,
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				err := setNamespace(test.defaultNamespace, test.additionalNamespace)
				require.NoError(t, err)
				ns := GetNamespaces()
				require.Len(t, ns, test.expected, "Length of namespaces mismatch")
			})
		}
	})
}

func getFissionNamespaces(builderNS, functionNS, defaultNS string) *NamespaceResolver {
	return &NamespaceResolver{
		FunctionNamespace: functionNS,
		BuilderNamespace:  builderNS,
		DefaultNamespace:  defaultNS,
	}
}

func setNamespace(defaultNamespace string, additionalNamespace string) error {
	err := os.Setenv(ENV_DEFAULT_NAMESPACE, defaultNamespace)
	if err != nil {
		return err
	}
	return os.Setenv(ENV_ADDITIONAL_NAMESPACE, additionalNamespace)
}

// TestNamespaceResolverDynamicSet covers the runtime-mutable resource-namespace
// set introduced for multi-namespace tenancy (Phase 0): the set is read through
// a copy-returning accessor and mutated through Set/Add/Remove, which publish a
// coalesced change signal to subscribers. See docs/multiple-namespace/prd.md §4.2.
func TestNamespaceResolverDynamicSet(t *testing.T) {
	t.Run("FissionResourceNamespaces returns a copy", func(t *testing.T) {
		r := &NamespaceResolver{}
		r.SetTenants(map[string]string{"ns-a": "ns-a"})

		got := r.FissionResourceNamespaces()
		got["ns-b"] = "ns-b" // mutating the returned map must not leak back

		again := r.FissionResourceNamespaces()
		require.NotContains(t, again, "ns-b", "accessor must return a defensive copy")
		require.Contains(t, again, "ns-a")
	})

	t.Run("SetTenants replaces the whole set", func(t *testing.T) {
		r := &NamespaceResolver{}
		r.SetTenants(map[string]string{"ns-a": "ns-a", "ns-b": "ns-b"})
		r.SetTenants(map[string]string{"ns-c": "ns-c"})

		require.Equal(t, map[string]string{"ns-c": "ns-c"}, r.FissionResourceNamespaces())
	})

	t.Run("AddTenant and RemoveTenant mutate the set", func(t *testing.T) {
		r := &NamespaceResolver{}
		r.AddTenant("ns-a")
		r.AddTenant("ns-b")

		got := r.FissionResourceNamespaces()
		require.Len(t, got, 2)
		require.Contains(t, got, "ns-a")
		require.Contains(t, got, "ns-b")

		r.RemoveTenant("ns-a")
		got = r.FissionResourceNamespaces()
		require.Len(t, got, 1)
		require.Contains(t, got, "ns-b")
	})

	t.Run("IsTenant reflects the live set", func(t *testing.T) {
		r := &NamespaceResolver{}
		r.SetTenants(map[string]string{"team-a": "team-a"})

		assert.True(t, r.IsTenant("team-a"))
		assert.False(t, r.IsTenant("team-b"))

		r.AddTenant("team-b")
		assert.True(t, r.IsTenant("team-b"), "IsTenant must observe a later AddTenant")

		r.RemoveTenant("team-a")
		assert.False(t, r.IsTenant("team-a"), "IsTenant must observe a RemoveTenant")
	})
}

// TestNamespaceResolverConcurrentAccess is the thread-safety guard: concurrent
// readers and writers must be race-free under `go test -race`.
func TestNamespaceResolverConcurrentAccess(t *testing.T) {
	r := &NamespaceResolver{}
	r.SetTenants(map[string]string{"seed": "seed"})

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			ns := fmt.Sprintf("ns-%d", i)
			r.AddTenant(ns)
			_ = r.FissionResourceNamespaces()
			_ = r.FunctionNamespaces()
			r.RemoveTenant(ns)
		})
	}
	for range 8 {
		wg.Go(func() {
			for range 50 {
				_ = r.FissionResourceNamespaces()
			}
		})
	}
	wg.Wait()

	require.Contains(t, r.FissionResourceNamespaces(), "seed", "seed namespace must survive the churn")
}
