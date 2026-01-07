package utils

import (
	"os"
	"testing"

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

func TestWatchAllNamespaces(t *testing.T) {
	// Save current state
	oldVal := os.Getenv(ENV_WATCH_ALL_NAMESPACES)
	defer os.Setenv(ENV_WATCH_ALL_NAMESPACES, oldVal)

	// Set env var to true
	os.Setenv(ENV_WATCH_ALL_NAMESPACES, "true")

	// Verify GetNamespaces returns meta_v1.NamespaceAll
	ns := GetNamespaces()
	require.Len(t, ns, 1)
	require.Contains(t, ns, "") // NamespaceAll is empty string
	require.Equal(t, "", ns[""])

	// Set env var to false
	os.Setenv(ENV_WATCH_ALL_NAMESPACES, "false")
	// Should fall back to default behavior (defaults to 'default' if nothing else set)
	os.Setenv(ENV_DEFAULT_NAMESPACE, "default")
	os.Setenv(ENV_ADDITIONAL_NAMESPACE, "")
	
	ns = GetNamespaces()
	require.Len(t, ns, 1)
	require.Contains(t, ns, "default")
}
