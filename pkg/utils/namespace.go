package utils

import (
	"os"
	"strings"

	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/utils/loggerfactory"
)

const (
	ENV_FUNCTION_NAMESPACE   string = "FISSION_FUNCTION_NAMESPACE"
	ENV_BUILDER_NAMESPACE    string = "FISSION_BUILDER_NAMESPACE"
	ENV_DEFAULT_NAMESPACE    string = "FISSION_DEFAULT_NAMESPACE"
	ENV_ADDITIONAL_NAMESPACE string = "FISSION_RESOURCE_NAMESPACES"
)

type (
	NamespaceResolver struct {
		FunctionNamespace string
		BuilderNamespace  string
		DefaultNamespace  string
		FissionResourceNS map[string]string
		Logger            *zap.Logger
	}

	options struct {
		functionNS bool
		builderNS  bool
		defaultNs  bool
	}

	option func(options *options) *options
)

var nsResolver *NamespaceResolver

func init() {
	nsResolver = &NamespaceResolver{
		FunctionNamespace: os.Getenv(ENV_FUNCTION_NAMESPACE),
		BuilderNamespace:  os.Getenv(ENV_BUILDER_NAMESPACE),
		DefaultNamespace:  os.Getenv(ENV_DEFAULT_NAMESPACE),
		FissionResourceNS: GetNamespaces(),
		Logger:            loggerfactory.GetLogger(),
	}

	nsResolver.Logger.Debug("namespaces", zap.String("function_namespace", nsResolver.FunctionNamespace),
		zap.String("builder_namespace", nsResolver.BuilderNamespace),
		zap.String("default_namespace", nsResolver.DefaultNamespace),
		zap.Any("fission_resource_namespace", listNamespaces(nsResolver.FissionResourceNS)))
}

// listNamespaces => convert namespaces from map to slice
func listNamespaces(namespaces map[string]string) []string {
	ns := make([]string, 0)
	for _, namespace := range namespaces {
		ns = append(ns, namespace)
	}
	return ns
}

func WithBuilderNs() option {
	return func(options *options) *options {
		options.builderNS = true
		return options
	}
}

func WithFunctionNs() option {
	return func(options *options) *options {
		options.functionNS = true
		return options
	}
}

func WithDefaultNs() option {
	return func(options *options) *options {
		options.defaultNs = true
		return options
	}
}

func (nsr *NamespaceResolver) FissionNSWithOptions(option ...option) map[string]string {
	var options options
	for _, opt := range option {
		options = *opt(&options)
	}

	fissionResourceNS := make(map[string]string)
	for k, v := range nsr.FissionResourceNS {
		fissionResourceNS[k] = v
	}

	if options.functionNS && nsr.FunctionNamespace != "" {
		fissionResourceNS[nsr.FunctionNamespace] = nsr.FunctionNamespace
	}
	if options.builderNS && nsr.BuilderNamespace != "" {
		fissionResourceNS[nsr.BuilderNamespace] = nsr.BuilderNamespace
	}
	if options.defaultNs && nsr.DefaultNamespace != "" {
		fissionResourceNS[nsr.DefaultNamespace] = nsr.DefaultNamespace
	}
	nsr.Logger.Debug("fission resource namespaces", zap.Any("namespaces", listNamespaces(fissionResourceNS)))
	return fissionResourceNS
}

func GetNamespaces() map[string]string {
	namespaces := make(map[string]string)

	envValue := os.Getenv(ENV_DEFAULT_NAMESPACE)
	if len(envValue) > 0 {
		namespaces[envValue] = envValue
	}

	envValue = os.Getenv(ENV_ADDITIONAL_NAMESPACE)
	if len(envValue) > 0 {
		lstNamespaces := strings.Split(envValue, ",")
		for _, namespace := range lstNamespaces {
			// check to handle string with additional comma at the end of string. eg- ns1,ns2,
			if namespace != "" {
				namespaces[namespace] = namespace
			}
		}
	}

	if len(namespaces) == 0 {
		namespaces[metav1.NamespaceDefault] = metav1.NamespaceDefault
	}
	return namespaces
}

func (nsr *NamespaceResolver) GetBuilderNS(namespace string) string {
	if nsr.BuilderNamespace == "" {
		return namespace
	}

	if namespace != metav1.NamespaceDefault {
		return namespace
	}
	return nsr.BuilderNamespace
}

func (nsr *NamespaceResolver) GetFunctionNS(namespace string) string {
	if nsr.FunctionNamespace == "" {
		return namespace
	}

	if namespace != metav1.NamespaceDefault {
		return namespace
	}
	return nsr.FunctionNamespace
}

func (nsr *NamespaceResolver) ResolveNamespace(namespace string) string {
	if nsr.FunctionNamespace == "" || nsr.BuilderNamespace == "" {
		return nsr.DefaultNamespace
	}
	return namespace
}

// GetFissionNamespaces => return all fission core component namespaces
func DefaultNSResolver() *NamespaceResolver {
	return nsResolver
}
