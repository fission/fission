package utils

import (
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ENV_FUNCTION_NAMESPACE string = "FISSION_FUNCTION_NAMESPACE"
	ENV_BUILDER_NAMESPACE  string = "FISSION_BUILDER_NAMESPACE"
	ENV_DEFAULT_NAMESPACE  string = "FISSION_DEFAULT_NAMESPACE"
)

type NamespaceResolver struct {
	FunctionNamespace string
	BuiderNamespace   string
	DefaultNamespace  string
}

func (nsr *NamespaceResolver) GetBuilderNS(namespace string) string {
	if nsr.FunctionNamespace == "" || nsr.BuiderNamespace == "" {
		return namespace
	}

	var ns string
	ns = nsr.BuiderNamespace
	if namespace != metav1.NamespaceDefault {
		ns = namespace
	}
	return ns
}

func (nsr *NamespaceResolver) GetFunctionNS(namespace string) string {
	if nsr.FunctionNamespace == "" || nsr.BuiderNamespace == "" {
		return namespace
	}

	ns := nsr.FunctionNamespace
	if namespace != metav1.NamespaceDefault {
		ns = namespace
	}
	return ns
}

func (nsr *NamespaceResolver) ResolveNamespace(namespace string) string {
	if nsr.FunctionNamespace == "" || nsr.BuiderNamespace == "" {
		return nsr.DefaultNamespace
	}
	return namespace
}

// GetFissionNamespaces => return all fission core component namespaces
func GetFissionNamespaces() *NamespaceResolver {
	return &NamespaceResolver{
		FunctionNamespace: os.Getenv(ENV_FUNCTION_NAMESPACE),
		BuiderNamespace:   os.Getenv(ENV_BUILDER_NAMESPACE),
		DefaultNamespace:  os.Getenv(ENV_DEFAULT_NAMESPACE),
	}
}
