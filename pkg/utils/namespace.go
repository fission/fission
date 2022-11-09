package utils

import (
	"os"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type NamespaceResolver struct {
	FunctionNamespace string
	BuiderNamespace   string
	DefaultNamespace  string
}

func (nsr *NamespaceResolver) GetBuilderNS(namespace string) string {
	if nsr.FunctionNamespace == "" || nsr.BuiderNamespace == "" {
		return nsr.DefaultNamespace
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
		return nsr.DefaultNamespace
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
		FunctionNamespace: os.Getenv(v1.ENV_FUNCTION_NAMESPACE),
		BuiderNamespace:   os.Getenv(v1.ENV_FUNCTION_NAMESPACE),
		DefaultNamespace:  os.Getenv(v1.ENV_DEFAULT_NAMESPACE),
	}
}
