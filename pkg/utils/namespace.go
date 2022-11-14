package utils

import (
	"os"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type FissionNamespace struct {
	FunctionNamespace string
	BuiderNamespace   string
	DefaultNamespace  string
}

func (fn *FissionNamespace) GetNamespace(namespace, kind string) string {
	if fn.FunctionNamespace == "" || fn.BuiderNamespace == "" {
		return fn.DefaultNamespace
	}
	var ns string
	switch kind {
	case v1.BuilderNamespace:
		ns = fn.BuiderNamespace
		if namespace != metav1.NamespaceDefault {
			ns = namespace
		}
	case v1.FunctionNamespace:
		ns = fn.FunctionNamespace
		if namespace != metav1.NamespaceDefault {
			ns = namespace
		}
	default:
		panic("unknown kind: " + kind)
	}
	return ns
}

func (fn *FissionNamespace) ResolveNamespace(namespace, kind string) string {
	if fn.FunctionNamespace == "" || fn.BuiderNamespace == "" {
		return namespace
	}
	var ns string
	switch kind {
	case v1.BuilderNamespace:
		ns = fn.BuiderNamespace
	case v1.FunctionNamespace:
		ns = fn.FunctionNamespace
	default:
		panic("unknown kind: " + kind)
	}
	return ns
}

// GetFissionNamespaces => return all fission core component namespaces
func GetFissionNamespaces() *FissionNamespace {
	return &FissionNamespace{
		FunctionNamespace: os.Getenv(v1.ENV_FUNCTION_NAMESPACE),
		BuiderNamespace:   os.Getenv(v1.ENV_FUNCTION_NAMESPACE),
		DefaultNamespace:  os.Getenv(v1.ENV_DEFAULT_NAMESPACE),
	}
}
