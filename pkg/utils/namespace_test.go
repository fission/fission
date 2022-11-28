package utils

import "testing"

func TestNamespaceResolver(t *testing.T) {
	t.Run("GetBuilderNS", func(t *testing.T) {
		for _, test := range []struct {
			name              string
			namespaceResolver *FissionNS
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
				namespaceResolver: getFissionNamespaces("fission-builder", "fission-function", "testns"),
				namespace:         "testns2",
				expected:          "testns2",
			},
			{
				name:              "should return testns3 namespace",
				namespaceResolver: getFissionNamespaces("", "", "testns"),
				namespace:         "testns3",
				expected:          "testns3",
			},
			{
				name:              "should return default namespace",
				namespaceResolver: getFissionNamespaces("fission-builder", "", "default"),
				namespace:         "default",
				expected:          "default",
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
			namespaceResolver *FissionNS
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
				namespaceResolver: getFissionNamespaces("fission-builder", "fission-function", "testns"),
				namespace:         "testns2",
				expected:          "testns2",
			},
			{
				name:              "should return testns3 namespace",
				namespaceResolver: getFissionNamespaces("", "", "testns"),
				namespace:         "testns3",
				expected:          "testns3",
			},
			{
				name:              "should return default namespace",
				namespaceResolver: getFissionNamespaces("", "fission-function", "default"),
				namespace:         "default",
				expected:          "default",
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
			namespaceResolver *FissionNS
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
				if ns != test.expected {
					t.Errorf("expected function namespace %s, got %s", test.expected, ns)
				}
			})
		}
	})
}

func getFissionNamespaces(builderNS, functionNS, defaultNS string) *FissionNS {
	return &FissionNS{
		FunctionNamespace: functionNS,
		BuiderNamespace:   builderNS,
		DefaultNamespace:  defaultNS,
	}
}
