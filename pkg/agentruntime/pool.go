// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// pool.go implements PoolAPI, the read-only pool-introspection surface: GET
// /registry/pool. Unlike RegistryAPI (Task 18), which reads only the agent
// registry and session store, PoolAPI reads the executor's actual warm/
// specialized pod topology (via the Manager's Pod cache — see main.go's
// checkPoolRBAC / poolCacheOptions) plus the router's own EndpointSlice-
// derived endpoint index (pkg/router/endpointcache), so a caller can see
// which pods exist for a Function — including warm, unspecialized pool pods
// that never appear in an EndpointSlice, since no function Service selects
// them until they are claimed.
package agentruntime

import (
	"net/http"
	"slices"
	"strings"

	"github.com/go-logr/logr"
	"github.com/modelcontextprotocol/go-sdk/auth"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/endpointcache"
)

// PodSummary is one pod in GET /registry/pool's "pods" list — enough to tell
// a warm-unspecialized pool pod from a specialized/served one without
// exposing the pod's full spec.
type PodSummary struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	IP        string `json:"ip,omitempty"`
	Phase     string `json:"phase"`
	Ready     bool   `json:"ready"`
	// Environment is fv1.ENVIRONMENT_NAME — empty only if the pod predates
	// that label (should not happen for any pool/executor pod).
	Environment string `json:"environment,omitempty"`
	// Served mirrors fv1.SERVED_LABEL: false for a warm, unclaimed pool pod;
	// true once the post-specialization patch has run (gp.go) and the pod is
	// eligible to join its function Service's EndpointSlice.
	Served bool `json:"served"`
	// FunctionGeneration mirrors fv1.FUNCTION_GENERATION; empty for a
	// warm-unspecialized pod (it carries no function labels yet).
	FunctionGeneration string `json:"functionGeneration,omitempty"`
	// Provisioned mirrors fv1.PROVISIONED_LABEL (RFC-0026): true for a served
	// pod the provisioner is actively keeping warm (idle-reaper exempt).
	Provisioned bool `json:"provisioned"`
}

// EndpointSummary is one ready-or-not endpoint in GET /registry/pool's
// "endpoints" breakdown, projected from endpointcache.Endpoint.
type EndpointSummary struct {
	Address string `json:"address"`
	PodUID  string `json:"podUID,omitempty"`
	Ready   bool   `json:"ready"`
}

// poolResponse is the wire shape of GET /registry/pool.
type poolResponse struct {
	Pods []PodSummary `json:"pods"`
	// Endpoints is keyed "<namespace>/<name>", one entry per agent-enabled
	// Function this replica's AgentView knows about that has at least one
	// endpoint in the index — never every Function in the cluster, since the
	// pool panel is scoped to agent introspection, not general Fission
	// topology.
	Endpoints map[string][]EndpointSummary `json:"endpoints"`
}

// PoolAPI serves GET /registry/pool. Like RegistryAPI it never mutates
// anything; unlike RegistryAPI it reads two additional, independently
// degradable dependencies (see synced/degraded below).
type PoolAPI struct {
	logger logr.Logger
	client client.Client
	index  *endpointcache.Index
	view   *AgentView
	authz  *Authorizer
	// synced reports whether the pool cache (EndpointSlice/Pod informers) has
	// completed its initial replay. nil or false while warming, or
	// permanently if degraded (no informer was ever registered) — see
	// main.go's Start.
	synced func() bool
	// degraded reports whether the startup RBAC preflight (checkPoolRBAC)
	// failed. Checked BEFORE synced so the two 503 cases carry distinct,
	// actionable messages instead of collapsing into one "not ready" body.
	degraded func() bool
}

// NewPoolAPI returns a PoolAPI. c is the Manager's cached client (Pod reads);
// index is the shared endpointcache.Index fed by endpointcache.RegisterInformer
// (or left permanently empty when degraded); view supplies the set of
// agent-enabled Functions the "endpoints" breakdown iterates. synced and
// degraded are read on every request, never cached at construction time,
// since both flip after Start begins serving.
func NewPoolAPI(logger logr.Logger, c client.Client, index *endpointcache.Index, view *AgentView, authz *Authorizer, synced func() bool, degraded func() bool) *PoolAPI {
	return &PoolAPI{logger: logger, client: c, index: index, view: view, authz: authz, synced: synced, degraded: degraded}
}

// ServePool serves GET /registry/pool. It carries no {namespace} path value
// (pod topology spans every namespace this replica's cache watches), so
// main.go mounts it behind authz.HTTPMiddleware and the namespace scope is
// applied here manually, exactly like RegistryAPI.ListAgents.
func (p *PoolAPI) ServePool(w http.ResponseWriter, r *http.Request) {
	// RBAC-degraded takes priority over cache-warming: when the preflight
	// failed, no informer was ever registered, so synced() would also report
	// false forever — checking degraded first gives callers the actionable
	// "missing RBAC" message instead of a permanent, indistinguishable
	// "warming" 503.
	if p.degraded != nil && p.degraded() {
		writeErr(w, http.StatusServiceUnavailable, "pool introspection degraded: missing RBAC (endpointslices/pods)")
		return
	}
	if p.synced == nil || !p.synced() {
		writeErr(w, http.StatusServiceUnavailable, "pool cache warming")
		return
	}

	scope, ok := p.authz.ScopeFromTokenInfo(auth.TokenInfoFromContext(r.Context()))
	if !ok {
		writeErr(w, http.StatusForbidden, "forbidden: token not authorized for any namespace")
		return
	}

	var podList corev1.PodList
	if err := p.client.List(r.Context(), &podList); err != nil {
		p.logger.Error(err, "listing pods failed", "operation", "ServePool")
		writeErr(w, http.StatusInternalServerError, "listing pods")
		return
	}
	pods := make([]PodSummary, 0, len(podList.Items))
	for i := range podList.Items {
		pod := &podList.Items[i]
		if !scope.Allows(pod.Namespace) {
			continue
		}
		pods = append(pods, podSummaryFrom(pod))
	}
	slices.SortFunc(pods, func(a, b PodSummary) int {
		if c := strings.Compare(a.Namespace, b.Namespace); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})

	endpoints := map[string][]EndpointSummary{}
	if p.index != nil {
		for _, e := range p.view.List() {
			if !scope.Allows(e.Namespace) {
				continue
			}
			eps := p.index.Lookup(e.Namespace, e.Name, "")
			if len(eps) == 0 {
				continue
			}
			summaries := make([]EndpointSummary, 0, len(eps))
			for _, ep := range eps {
				summaries = append(summaries, EndpointSummary{
					Address: ep.Address,
					PodUID:  string(ep.PodUID),
					Ready:   ep.Ready,
				})
			}
			slices.SortFunc(summaries, func(a, b EndpointSummary) int {
				return strings.Compare(a.Address, b.Address)
			})
			endpoints[e.Namespace+"/"+e.Name] = summaries
		}
	}

	writeJSON(w, http.StatusOK, poolResponse{Pods: pods, Endpoints: endpoints})
}

// podReady reports whether pod's PodReady condition is True. A pod with no
// conditions yet (freshly created) is not ready.
func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// podSummaryFrom projects a cached Pod into the wire shape, reading only the
// const.go label keys the brief calls out (SERVED_LABEL, FUNCTION_GENERATION,
// PROVISIONED_LABEL, ENVIRONMENT_NAME) — never the full pod spec.
func podSummaryFrom(pod *corev1.Pod) PodSummary {
	return PodSummary{
		Namespace:          pod.Namespace,
		Name:               pod.Name,
		IP:                 pod.Status.PodIP,
		Phase:              string(pod.Status.Phase),
		Ready:              podReady(pod),
		Environment:        pod.Labels[fv1.ENVIRONMENT_NAME],
		Served:             pod.Labels[fv1.SERVED_LABEL] == fv1.SERVED_VALUE,
		FunctionGeneration: pod.Labels[fv1.FUNCTION_GENERATION],
		Provisioned:        pod.Labels[fv1.PROVISIONED_LABEL] == fv1.PROVISIONED_VALUE,
	}
}
