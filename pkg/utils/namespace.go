package utils

import (
	"os"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ENV_FUNCTION_NAMESPACE   string = "FISSION_FUNCTION_NAMESPACE"
	ENV_BUILDER_NAMESPACE    string = "FISSION_BUILDER_NAMESPACE"
	ENV_DEFAULT_NAMESPACE    string = "FISSION_DEFAULT_NAMESPACE"
	ENV_ADDITIONAL_NAMESPACE string = "FISSION_RESOURCE_NAMESPACES"
)

type FissionNS struct {
	FunctionNamespace string
	BuiderNamespace   string
	DefaultNamespace  string
	ResourceNS        []string
}

var fissionNS *FissionNS

func init() {
	fissionNS = &FissionNS{
		FunctionNamespace: os.Getenv(ENV_FUNCTION_NAMESPACE),
		BuiderNamespace:   os.Getenv(ENV_BUILDER_NAMESPACE),
		DefaultNamespace:  os.Getenv(ENV_DEFAULT_NAMESPACE),
		ResourceNS:        getNamespaces(),
	}
}

func getNamespaces() []string {
	envValue := os.Getenv(ENV_ADDITIONAL_NAMESPACE)
	if len(envValue) == 0 {
		return []string{
			metav1.NamespaceDefault,
		}
	}

	namespaces := make([]string, 0)
	lstNamespaces := strings.Split(envValue, ",")
	for _, namespace := range lstNamespaces {
		//check to handle string with additional comma at the end of string. eg- ns1,ns2,
		if namespace != "" {
			namespaces = append(namespaces, namespace)
		}
	}
	return namespaces
}

func (nsr *FissionNS) GetBuilderNS(namespace string) string {
	if nsr.FunctionNamespace == "" || nsr.BuiderNamespace == "" {
		return namespace
	}

	if namespace != metav1.NamespaceDefault {
		return namespace
	}
	return nsr.BuiderNamespace
}

func (nsr *FissionNS) GetFunctionNS(namespace string) string {
	if nsr.FunctionNamespace == "" || nsr.BuiderNamespace == "" {
		return namespace
	}

	if namespace != metav1.NamespaceDefault {
		return namespace
	}
	return nsr.FunctionNamespace
}

func (nsr *FissionNS) ResolveNamespace(namespace string) string {
	if nsr.FunctionNamespace == "" || nsr.BuiderNamespace == "" {
		return nsr.DefaultNamespace
	}
	return namespace
}

// GetFissionNamespaces => return all fission core component namespaces
func GetNamespaces() *FissionNS {
	return fissionNS
}
