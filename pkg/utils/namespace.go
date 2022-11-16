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

var nsResolver *NamespaceResolver

func init() {
	nsResolver = &NamespaceResolver{
		FunctionNamespace: os.Getenv(ENV_FUNCTION_NAMESPACE),
		BuiderNamespace:   os.Getenv(ENV_BUILDER_NAMESPACE),
		DefaultNamespace:  os.Getenv(ENV_DEFAULT_NAMESPACE),
	}
}

func (nsr *NamespaceResolver) GetBuilderNS(namespace string) string {
	if nsr.FunctionNamespace == "" || nsr.BuiderNamespace == "" {
		return namespace
	}

	if namespace != metav1.NamespaceDefault {
		return namespace
	}
	return nsr.BuiderNamespace
}

func (nsr *NamespaceResolver) GetFunctionNS(namespace string) string {
	if nsr.FunctionNamespace == "" || nsr.BuiderNamespace == "" {
		return namespace
	}

	if namespace != metav1.NamespaceDefault {
		return namespace
	}
	return nsr.FunctionNamespace
}

func (nsr *NamespaceResolver) ResolveNamespace(namespace string) string {
	if nsr.FunctionNamespace == "" || nsr.BuiderNamespace == "" {
		return nsr.DefaultNamespace
	}
	return namespace
}

// GetFissionNamespaces => return all fission core component namespaces
func DefaultNSResolver() *NamespaceResolver {
	return nsResolver
}
