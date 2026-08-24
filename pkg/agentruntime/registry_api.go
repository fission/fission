// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// registry_api.go implements RegistryAPI, the read-only introspection
// surface over the agent registry: the list of agent-enabled Functions this
// replica's AgentView knows about, and their sessions (backed by
// SessionStore). It is a separate mux registrant from Dispatcher (which
// serves the write path, POST /agents/{namespace}/{name}) because the
// per-route auth wiring differs — see the doc comments on ListAgents,
// ListSessions, and GetSession, and main.go's mounting of all three.
package agentruntime

import (
	"encoding/json/v2"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/fission/fission/pkg/statestore"
)

const (
	// defaultSessionListLimit is applied when the sessions list route's
	// ?limit= query param is absent or non-positive.
	defaultSessionListLimit = 100
	// maxSessionListLimit caps ?limit= regardless of what the caller asks for.
	maxSessionListLimit = 500
)

// RegistryAPI serves GET /registry/... : read-only introspection over the
// agent registry. Unlike Dispatcher, it never mutates a SessionRecord.
type RegistryAPI struct {
	view  *AgentView
	store *SessionStore
	authz *Authorizer
}

// NewRegistryAPI returns a RegistryAPI reading from view and store, filtering
// GET /registry/agents by authz's verified scope (see ListAgents).
func NewRegistryAPI(view *AgentView, store *SessionStore, authz *Authorizer) *RegistryAPI {
	return &RegistryAPI{view: view, store: store, authz: authz}
}

// agentSummary is the wire shape of one entry in ListAgents's response.
// Durations are encoded as Go duration strings (e.g. "5m0s"), matching how an
// operator would pass them back via AgentConfig.IdleAfter/ArchiveAfter.
type agentSummary struct {
	Namespace     string `json:"namespace"`
	Name          string `json:"name"`
	SessionSource string `json:"sessionSource"`
	SessionName   string `json:"sessionName"`
	IdleAfter     string `json:"idleAfter"`
	ArchiveAfter  string `json:"archiveAfter"`
	MaxSessions   int64  `json:"maxSessions"`
}

// agentsResponse is the wire shape of ListAgents's response — a named type
// so the JSON envelope, like sessionsResponse below, never has to fall back
// to a map[string]any.
type agentsResponse struct {
	Agents []agentSummary `json:"agents"`
}

// ListAgents serves GET /registry/agents: every agent-enabled Function this
// replica's AgentView knows about, filtered to the caller's namespace scope.
//
// This route carries no {namespace} path value (it lists across namespaces),
// so it cannot use authz.Middleware, which reads r.PathValue("namespace") —
// main.go mounts it behind authz.HTTPMiddleware instead (bearer-token
// verification only), and the namespace filter is applied here manually via
// ScopeFromTokenInfo, exactly as authz.go's package doc prescribes. A scope
// that fails to resolve (defense in depth: HTTPMiddleware should already have
// rejected an invalid/missing token when auth is enabled) gets 403, not an
// empty list — an empty list would be indistinguishable from "no agents are
// registered".
func (a *RegistryAPI) ListAgents(w http.ResponseWriter, r *http.Request) {
	scope, ok := a.authz.ScopeFromTokenInfo(auth.TokenInfoFromContext(r.Context()))
	if !ok {
		writeErr(w, http.StatusForbidden, "forbidden: token not authorized for any namespace")
		return
	}

	entries := a.view.List()
	agents := make([]agentSummary, 0, len(entries))
	for _, e := range entries {
		if !scope.Allows(e.Namespace) {
			continue
		}
		agents = append(agents, agentSummary{
			Namespace:     e.Namespace,
			Name:          e.Name,
			SessionSource: string(e.SessionSource),
			SessionName:   e.SessionName,
			IdleAfter:     e.IdleAfter.String(),
			ArchiveAfter:  e.ArchiveAfter.String(),
			MaxSessions:   e.MaxSessions,
		})
	}
	// AgentView.List() makes no ordering guarantee; sort so the response is
	// deterministic for callers and tests.
	slices.SortFunc(agents, func(x, y agentSummary) int {
		if c := strings.Compare(x.Namespace, y.Namespace); c != 0 {
			return c
		}
		return strings.Compare(x.Name, y.Name)
	})

	writeJSON(w, http.StatusOK, agentsResponse{Agents: agents})
}

// resolveSessionListLimit parses the ListSessions route's ?limit= query
// value: "" or a non-positive integer falls back to
// defaultSessionListLimit, anything above maxSessionListLimit is clamped
// down to it, and a value that fails to parse as an integer is reported via
// ok=false so the caller can answer 400. Extracted from ListSessions so the
// parse/default/clamp policy has a single, directly unit-testable seam.
func resolveSessionListLimit(raw string) (limit int, ok bool) {
	if raw == "" {
		return defaultSessionListLimit, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	if n <= 0 {
		return defaultSessionListLimit, true
	}
	if n > maxSessionListLimit {
		return maxSessionListLimit, true
	}
	return n, true
}

// sessionsResponse is the wire shape of ListSessions's response. Next is
// omitted (rather than an empty string) once the caller has reached the last
// page.
type sessionsResponse struct {
	Sessions []SessionRecord `json:"sessions"`
	Next     string          `json:"next,omitempty"`
}

// ListSessions serves GET /registry/agents/{namespace}/{name}/sessions,
// paging through SessionStore.List for the given agent. It carries a
// {namespace} path value, so main.go mounts it behind authz.Middleware (which
// enforces the namespace scope before this handler ever runs) rather than the
// manual filter ListAgents uses.
func (a *RegistryAPI) ListSessions(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	name := r.PathValue("name")

	if _, ok := a.view.Lookup(ns, name); !ok {
		writeErr(w, http.StatusNotFound, "not an agent function")
		return
	}

	limit, ok := resolveSessionListLimit(r.URL.Query().Get("limit"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid limit")
		return
	}

	sessions, next, err := a.store.List(r.Context(), ns, name, r.URL.Query().Get("page"), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "listing sessions")
		return
	}

	writeJSON(w, http.StatusOK, sessionsResponse{Sessions: sessions, Next: next})
}

// GetSession serves GET /registry/agents/{namespace}/{name}/sessions/{id}.
// Like ListSessions, it carries a {namespace} path value and is mounted
// behind authz.Middleware in main.go.
func (a *RegistryAPI) GetSession(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	name := r.PathValue("name")
	id := r.PathValue("id")

	if _, ok := a.view.Lookup(ns, name); !ok {
		writeErr(w, http.StatusNotFound, "not an agent function")
		return
	}

	// Validated with the dispatcher's own sessionIDPattern, and BEFORE the
	// store call: a caller-supplied id is a statestore key, so an
	// out-of-pattern id is rejected here rather than handed to the store.
	if !sessionIDPattern.MatchString(id) {
		writeErr(w, http.StatusBadRequest, "invalid session id")
		return
	}

	rec, _, err := a.store.Get(r.Context(), ns, name, id)
	if err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "session not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "getting session")
		return
	}

	writeJSON(w, http.StatusOK, rec)
}

// writeJSON answers with v marshaled as the JSON body at status. It mirrors
// writeErr's shape (dispatcher.go) for the success path. It is generic
// (rather than taking v any) so every call site's argument type is visible
// in its own signature instead of being erased to any at this boundary.
func writeJSON[T any](w http.ResponseWriter, status int, v T) {
	b, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
