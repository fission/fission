// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"maps"
	"os"
	"strconv"
	"strings"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/go-logr/logr"

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

		// mu guards fissionResourceNS and subscribers, making the resource
		// namespace set safe to mutate at runtime for multi-namespace tenancy
		// (Phase 0; see docs/multiple-namespace/prd.md §4.2). The three scalar
		// namespaces above stay env-driven and immutable — only the tenant set
		// is dynamic.
		mu                sync.RWMutex
		fissionResourceNS map[string]string
		subscribers       []chan struct{}

		Logger logr.Logger
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
		fissionResourceNS: GetNamespaces(),
		Logger:            loggerfactory.GetLogger(),
	}

	nsResolver.Logger.V(1).Info("namespaces", "function_namespace", nsResolver.FunctionNamespace,
		"builder_namespace", nsResolver.BuilderNamespace,
		"default_namespace", nsResolver.DefaultNamespace,
		"fission_resource_namespace", listNamespaces(nsResolver.FissionResourceNamespaces()))
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

	fissionResourceNS := nsr.FissionResourceNamespaces()

	if options.functionNS && nsr.FunctionNamespace != "" {
		fissionResourceNS[nsr.FunctionNamespace] = nsr.FunctionNamespace
	}
	if options.builderNS && nsr.BuilderNamespace != "" {
		fissionResourceNS[nsr.BuilderNamespace] = nsr.BuilderNamespace
	}
	if options.defaultNs && nsr.DefaultNamespace != "" {
		fissionResourceNS[nsr.DefaultNamespace] = nsr.DefaultNamespace
	}
	nsr.Logger.V(1).Info("fission resource namespaces", "namespaces", listNamespaces(fissionResourceNS))
	return fissionResourceNS
}

// FissionResourceNamespaces returns a copy of the live set of namespaces whose
// Fission resources (Functions, Packages, Environments, Triggers) this process
// watches. The returned map is a defensive copy — callers may iterate or mutate
// it without affecting the resolver, and reads are safe against concurrent
// SetTenants/AddTenant/RemoveTenant.
func (nsr *NamespaceResolver) FissionResourceNamespaces() map[string]string {
	nsr.mu.RLock()
	defer nsr.mu.RUnlock()
	out := make(map[string]string, len(nsr.fissionResourceNS))
	maps.Copy(out, nsr.fissionResourceNS)
	return out
}

// IsTenant reports whether ns is in the live resource-namespace set. It is the
// cheap membership check (no map copy) used on the per-event hot path by the
// reconcilers' tenant-membership predicate.
func (nsr *NamespaceResolver) IsTenant(ns string) bool {
	nsr.mu.RLock()
	defer nsr.mu.RUnlock()
	_, ok := nsr.fissionResourceNS[ns]
	return ok
}

// SetTenants replaces the live resource-namespace set and notifies subscribers.
// The tenant-lifecycle controller calls this when the FissionTenant set changes,
// so the watched namespaces can be updated without a process restart.
func (nsr *NamespaceResolver) SetTenants(namespaces map[string]string) {
	nsr.mu.Lock()
	defer nsr.mu.Unlock()
	nsr.fissionResourceNS = make(map[string]string, len(namespaces))
	maps.Copy(nsr.fissionResourceNS, namespaces)
	nsr.notifyLocked()
}

// AddTenant adds ns to the live set, notifying subscribers only when ns was not
// already present. A no-op add is silent so subscribers aren't woken needlessly.
func (nsr *NamespaceResolver) AddTenant(ns string) {
	nsr.mu.Lock()
	defer nsr.mu.Unlock()
	if _, ok := nsr.fissionResourceNS[ns]; ok {
		return
	}
	if nsr.fissionResourceNS == nil {
		nsr.fissionResourceNS = make(map[string]string)
	}
	nsr.fissionResourceNS[ns] = ns
	nsr.notifyLocked()
}

// RemoveTenant removes ns from the live set, notifying subscribers only when ns
// was present.
func (nsr *NamespaceResolver) RemoveTenant(ns string) {
	nsr.mu.Lock()
	defer nsr.mu.Unlock()
	if _, ok := nsr.fissionResourceNS[ns]; !ok {
		return
	}
	delete(nsr.fissionResourceNS, ns)
	nsr.notifyLocked()
}

// Subscribe returns a channel that receives a coalesced signal whenever the
// resource-namespace set changes. The channel is buffered (depth 1) and sends
// are non-blocking, so a slow or absent reader never stalls a mutation and only
// ever observes "something changed", not how many times.
func (nsr *NamespaceResolver) Subscribe() <-chan struct{} {
	nsr.mu.Lock()
	defer nsr.mu.Unlock()
	ch := make(chan struct{}, 1)
	nsr.subscribers = append(nsr.subscribers, ch)
	return ch
}

// notifyLocked sends a non-blocking signal to every subscriber. Callers must
// hold nsr.mu.
func (nsr *NamespaceResolver) notifyLocked() {
	for _, ch := range nsr.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func GetNamespaces() map[string]string {
	namespaces := make(map[string]string)

	envValue := os.Getenv(ENV_DEFAULT_NAMESPACE)
	if len(envValue) > 0 {
		namespaces[envValue] = envValue
	}

	envValue = os.Getenv(ENV_ADDITIONAL_NAMESPACE)
	if len(envValue) > 0 {
		lstNamespaces := strings.SplitSeq(envValue, ",")
		for namespace := range lstNamespaces {
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

// FunctionNamespaces returns the deduplicated set of namespaces function
// workloads (pool pods, per-function Services, their EndpointSlices) live in:
// each Fission resource namespace mapped through GetFunctionNS. Shared by the
// router's slice watch/RBAC preflight and the executor's Service
// adoption/cleanup so all three iterate the same set by construction
// (RFC-0002); the Helm chart's router/role-dataplane.yaml mirrors the same
// mapping.
func (nsr *NamespaceResolver) FunctionNamespaces() []string {
	set := nsr.FissionResourceNamespaces()
	seen := make(map[string]struct{}, len(set))
	out := make([]string, 0, len(set))
	for _, ns := range set {
		fns := nsr.GetFunctionNS(ns)
		if _, ok := seen[fns]; ok {
			continue
		}
		seen[fns] = struct{}{}
		out = append(out, fns)
	}
	return out
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

// DynamicNamespacesEnabled reports whether the dynamic multi-namespace watch
// model is on (FISSION_DYNAMIC_NAMESPACES=true). When on, Fission-CRD caches are
// cluster-wide and reconcilers filter to the live tenant set, so namespaces can
// be onboarded/offboarded without a control-plane restart. Default off: the
// per-namespace caches and behaviour are unchanged.
func DynamicNamespacesEnabled() bool {
	v, _ := strconv.ParseBool(os.Getenv("FISSION_DYNAMIC_NAMESPACES"))
	return v
}
