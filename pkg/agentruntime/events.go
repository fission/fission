// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// events.go implements the SSE registry feed: GET /registry/events streams
// session and agent-list changes to connected clients.
//
// Session records live in the statestore's KV capability, which has no
// sequence number or change-notification primitive (G3: no Watch). So this
// feed is necessarily poll-diff, not push-on-write: a single background
// goroutine (Poller) periodically lists every known agent's live sessions,
// diffs the result against its own previous in-memory snapshot on (Status,
// LastActiveAt, Stats.Turns, CurrentPod), and publishes one "session" event
// per changed-or-new record — plus a bare "agents" event when the set of
// agent-enabled Functions itself changed — to a fan-out Broadcaster.
//
// This is the same best-effort stance RFC-0027's webhook publisher takes: a
// change that nets out to no diff between two polls (e.g. a counter bumped
// and bumped back within one AGENT_REGISTRY_POLL interval) is invisible to
// subscribers, and a client that needs the ground truth can always fall back
// to GET /registry/agents/{namespace}/{name}/sessions. There is also no
// ordering guarantee between a connect-time snapshot line and an
// already-buffered diff event for the same session id (see
// EventsHandler.ServeHTTP) — a replayed snapshot value can be one poll cycle
// stale relative to a diff sitting in the client's channel; the next poll
// heals it, same as any other missed-then-caught-up diff.
//
// The feed emits NO deletion events. When a session leaves the live
// keyspace — archived by the sweeper, or orphan-expired via its statestore
// TTL — it simply stops appearing in the next snapshot/diff cycle; no
// "removed" event is ever published for it. A subscriber that needs to know
// a session is gone (as opposed to merely unchanged since the last poll)
// must treat GET /registry/agents/{namespace}/{name}/sessions (or the
// per-session GET) as ground truth, the same fallback the previous
// paragraph already prescribes for a missed diff.
//
// A page-fetch error partway through paging one agent's live sessions
// (collectAgentSessions) never lets that agent's unread sessions vanish
// from the snapshot: the poller falls back to each such session's
// PREVIOUS-tick record (see seedFromPreviousSnapshot), so a transient
// SessionStore.List failure degrades to stale-for-one-tick — no event for
// an unchanged one, no spurious "new" event once the failure clears —
// rather than a false disappear-then-reappear.
package agentruntime

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// sseClientBufferSize is the per-client buffered channel capacity. A
	// client whose buffer is full when Broadcaster.publish tries to deliver
	// is dropped immediately (see Broadcaster's doc comment) — this is the
	// slow-consumer policy, not a tunable QoS knob.
	sseClientBufferSize = 64

	// sseKeepaliveInterval is how often ServeHTTP writes a ": keepalive"
	// comment line while otherwise idle, so an intermediary (or the client
	// itself) never mistakes a quiet connection for a dead one. This
	// substitutes for streaming.NewWatchdog, which exists to catch a
	// genuinely stuck upstream — this feed has no upstream to get stuck on,
	// only a ticker, so the watchdog's failure-detection role is moot here.
	sseKeepaliveInterval = 15 * time.Second

	// pollPageLimit is the per-agent SessionStore.List page size the Poller
	// uses to page through one agent's live sessions on each tick, mirroring
	// Sweeper's sweepPageLimit.
	pollPageLimit = 500
)

// agentsEventJSON is the fixed wire body of the "agents" event: a hint that
// the agent-enabled Function list may have changed, carrying no data of its
// own (a subscriber refetches GET /registry/agents, which re-enforces its
// own scope). It never changes, so it is a literal rather than something
// marshaled per publish.
var agentsEventJSON = []byte(`{"type":"agents"}`)

// sessionEvent is the wire shape of one "session" event: a session record
// that is new or has changed on the fields the Poller diffs.
type sessionEvent struct {
	Type   string        `json:"type"`
	Record SessionRecord `json:"record"`
}

// sessionKey identifies one session record across agents: session ids are
// only unique within one (namespace, agent) scope (see SessionStore's
// sessionScope), so the poller's snapshot keys on the full triple rather
// than risking an id collision across two different agents.
type sessionKey struct {
	namespace, agent, id string
}

// feedMessage is one broadcaster-queued SSE payload: eventType becomes the
// `event:` line, data the pre-marshaled JSON `data:` line.
type feedMessage struct {
	eventType string
	data      []byte
}

// sseClient is one subscriber: its delivery channel plus the namespace scope
// session events are filtered against at send time (Broadcaster.publish).
type sseClient struct {
	ch    chan feedMessage
	scope AuthScope
}

// Broadcaster fans out feedMessages to every subscribed sseClient. It is the
// only writer of a client's channel and the only closer of it, so a client
// reading from its channel can treat a closed channel as "this subscription
// has ended" unconditionally.
//
// publish never blocks the caller (the Poller's own goroutine) on a slow
// reader: delivery to each client is a non-blocking channel send, and a
// client whose buffer (cap sseClientBufferSize) is full is dropped —
// removed from the client set and its channel closed — rather than stalling
// delivery to every other client. This is a deliberate policy, not a bug:
// the feed is best-effort (see the package doc), and a reader too slow to
// keep its own buffer drained gets disconnected instead of holding up
// everyone else.
type Broadcaster struct {
	mu      sync.Mutex
	clients map[*sseClient]struct{}
}

// NewBroadcaster returns an empty Broadcaster.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{clients: map[*sseClient]struct{}{}}
}

// subscribe registers a new client scoped to scope. The caller must
// eventually call unsubscribe, even if the client was already dropped by
// publish (unsubscribe is a no-op in that case — see drop).
func (b *Broadcaster) subscribe(scope AuthScope) *sseClient {
	c := &sseClient{ch: make(chan feedMessage, sseClientBufferSize), scope: scope}
	b.mu.Lock()
	b.clients[c] = struct{}{}
	b.mu.Unlock()
	return c
}

// unsubscribe removes c and closes its channel, if it is still present.
func (b *Broadcaster) unsubscribe(c *sseClient) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.drop(c)
}

// drop removes c from the client set and closes its channel, if still
// present. Callers must hold b.mu. Factored out of unsubscribe and publish
// so exactly one of "the reader hung up" and "the buffer overflowed" ever
// performs the close, never both (a double close would panic).
func (b *Broadcaster) drop(c *sseClient) {
	if _, ok := b.clients[c]; ok {
		delete(b.clients, c)
		close(c.ch)
	}
}

// count returns the number of currently subscribed clients. Test-only
// observability seam (mirrors Sweeper.beforeCAS's rationale): production
// code has no need to introspect the live client count.
func (b *Broadcaster) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.clients)
}

// publish delivers msg to every subscribed client whose scope allows ns. An
// empty ns bypasses the filter entirely — used for the namespace-less
// "agents" event, which carries no per-record data for a scope check to
// apply to.
func (b *Broadcaster) publish(ns string, msg feedMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.clients {
		if ns != "" && !c.scope.Allows(ns) {
			continue
		}
		select {
		case c.ch <- msg:
		default:
			b.drop(c)
		}
	}
}

// Poller is the single background goroutine driving the SSE feed: it
// periodically lists every agent's live sessions, diffs against its own
// previous snapshot, and publishes changes to a Broadcaster. Its snapshot
// doubles as the read model GET /registry/events replays to a newly
// connected client (Snapshot) — no separate SessionStore.List round trip is
// needed on connect, and there is exactly one component in the whole
// process that ever calls SessionStore.List for this feed.
type Poller struct {
	logger   logr.Logger
	view     *AgentView
	store    *SessionStore
	bus      *Broadcaster
	interval time.Duration

	mu       sync.RWMutex
	sessions map[sessionKey]SessionRecord
	agents   map[types.NamespacedName]AgentEntry
}

// NewPoller returns a Poller that diffs store against view every interval,
// publishing changes to bus.
func NewPoller(logger logr.Logger, view *AgentView, store *SessionStore, bus *Broadcaster, interval time.Duration) *Poller {
	return &Poller{
		logger:   logger,
		view:     view,
		store:    store,
		bus:      bus,
		interval: interval,
		sessions: map[sessionKey]SessionRecord{},
		agents:   map[types.NamespacedName]AgentEntry{},
	}
}

// Run polls every p.interval until ctx is done, mirroring Sweeper.Run's
// ticker-loop shape exactly.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollOnce(ctx)
		}
	}
}

// Snapshot returns every session record in the poller's current snapshot
// that scope authorizes, for the connect-time replay ServeHTTP sends before
// streaming live diffs. Order is unspecified.
func (p *Poller) Snapshot(scope AuthScope) []SessionRecord {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]SessionRecord, 0, len(p.sessions))
	for _, rec := range p.sessions {
		if scope.Allows(rec.Namespace) {
			out = append(out, rec)
		}
	}
	return out
}

// pollOnce runs one list-diff-publish cycle across every agent in the view.
func (p *Poller) pollOnce(ctx context.Context) {
	entries := p.view.List()

	currentAgents := make(map[types.NamespacedName]AgentEntry, len(entries))
	for _, e := range entries {
		currentAgents[types.NamespacedName{Namespace: e.Namespace, Name: e.Name}] = e
	}

	currentSessions := make(map[sessionKey]SessionRecord)
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		p.collectAgentSessions(ctx, e, currentSessions)
	}

	p.mu.Lock()
	agentsChanged := !maps.Equal(p.agents, currentAgents)
	changed := diffSessions(p.sessions, currentSessions)
	p.agents = currentAgents
	p.sessions = currentSessions
	p.mu.Unlock()

	if agentsChanged {
		p.bus.publish("", feedMessage{eventType: "agents", data: agentsEventJSON})
	}
	for _, rec := range changed {
		data, err := json.Marshal(sessionEvent{Type: "session", Record: rec})
		if err != nil {
			p.logger.Error(err, "poller: marshaling session event failed", "namespace", rec.Namespace, "agent", rec.Agent, "session", rec.ID)
			continue
		}
		p.bus.publish(rec.Namespace, feedMessage{eventType: "session", data: data})
	}
}

// collectAgentSessions pages through e's entire live-session list, adding
// each record into out keyed by its full sessionKey. A list error is
// logged and ends this agent's page walk early — the next tick retries from
// the top rather than resuming a partial page, matching Sweeper.sweepAgent's
// own error handling. Unlike the sweeper, though, this walk feeds a diffed
// snapshot: a page fetched before the error already landed in out, but
// every session on a page this agent never reached is backfilled from the
// poller's PREVIOUS-tick snapshot (seedFromPreviousSnapshot) rather than
// left absent — see the package doc's paragraph on this. Without that
// backfill, a transient List failure would make those sessions vanish from
// this tick's diff (nothing published, since the poller-diff has no
// "removed" event) and then look spuriously "new" the moment the failure
// clears, because the record that would have proven "unchanged" was never
// in this tick's snapshot to compare against.
func (p *Poller) collectAgentSessions(ctx context.Context, e AgentEntry, out map[sessionKey]SessionRecord) {
	pageToken := ""
	for {
		if ctx.Err() != nil {
			return
		}
		recs, next, err := p.store.List(ctx, e.Namespace, e.Name, pageToken, pollPageLimit)
		if err != nil {
			p.logger.Error(err, "poller: list failed", "namespace", e.Namespace, "agent", e.Name)
			p.seedFromPreviousSnapshot(e, out)
			return
		}
		for _, rec := range recs {
			out[sessionKey{namespace: e.Namespace, agent: e.Name, id: rec.ID}] = rec
		}
		if next == "" {
			return
		}
		pageToken = next
	}
}

// seedFromPreviousSnapshot copies e's records from the poller's previous
// snapshot into out for any key collectAgentSessions has not already
// populated from a successfully fetched page. A key already in out came
// from a page fetched before the error and is more current than the stale
// copy, so it is left untouched.
func (p *Poller) seedFromPreviousSnapshot(e AgentEntry, out map[sessionKey]SessionRecord) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for k, rec := range p.sessions {
		if k.namespace != e.Namespace || k.agent != e.Name {
			continue
		}
		if _, ok := out[k]; !ok {
			out[k] = rec
		}
	}
}

// diffSessions returns every record in current that is new (no entry in
// prev) or changed on the diffed fields (see sessionUnchanged).
func diffSessions(prev, current map[sessionKey]SessionRecord) []SessionRecord {
	var changed []SessionRecord
	for k, rec := range current {
		if old, ok := prev[k]; !ok || !sessionUnchanged(old, rec) {
			changed = append(changed, rec)
		}
	}
	return changed
}

// sessionUnchanged reports whether old and cur are identical on the fields
// the poller diffs: Status, LastActiveAt, Stats.Turns, CurrentPod. Every
// other field the poller ignores is either immutable after Create
// (Namespace, Agent, ID, CreatedAt) or not yet meaningful for the feed
// (the rest of Stats). LastActiveAt is compared with Equal, not ==: both
// values here round-tripped through JSON, but Equal is the correct
// time.Time comparison regardless.
func sessionUnchanged(old, cur SessionRecord) bool {
	return old.Status == cur.Status &&
		old.LastActiveAt.Equal(cur.LastActiveAt) &&
		old.Stats.Turns == cur.Stats.Turns &&
		old.CurrentPod == cur.CurrentPod
}

// EventsHandler serves GET /registry/events: an SSE stream of session and
// agent-list changes. It is mounted BARE on main.go's outer mux — never
// behind authz.HTTPMiddleware/Middleware — because a browser EventSource
// cannot set request headers, which would make the Authorization bearer
// path permanently dead for the one client this route exists to serve.
// Authorization instead happens inside ServeHTTP: see bearerFromRequest and
// Authorizer.ScopeFromBearer.
//
// SECURITY: the ?token= query parameter (bearerFromRequest) carries the
// SAME full-privilege JWT that authorizes POST /agents/{namespace}/{name}
// dispatch — it is not a scoped-down, feed-only credential. Query strings
// are captured by most Ingress/Gateway/reverse-proxy access logs by
// default. Anything fronting this route in front of the cluster (an
// Ingress, a Gateway API HTTPRoute, a load balancer) MUST redact or
// suppress the token query parameter in its access logs, or the
// full-privilege token leaks into log storage on every browser EventSource
// connection. This is a mitigation, not a fix: the real fix is a
// short-TTL, single-use SSE ticket exchange (tracked as a follow-up, not
// implemented here) so the credential riding the URL is no longer the
// full-privilege dispatch token itself.
type EventsHandler struct {
	poller *Poller
	bus    *Broadcaster
	authz  *Authorizer
}

// NewEventsHandler returns an EventsHandler serving from poller/bus, using
// authz to resolve each connection's namespace scope.
func NewEventsHandler(poller *Poller, bus *Broadcaster, authz *Authorizer) *EventsHandler {
	return &EventsHandler{poller: poller, bus: bus, authz: authz}
}

// ServeHTTP authorizes the caller, subscribes it to bus, replays the
// poller's current snapshot (filtered to the caller's scope), then streams
// live diffs and periodic keepalive comments until the client disconnects.
//
// Subscribing BEFORE reading the snapshot (rather than after) is
// deliberate: it guarantees no event published after this connection is
// ever missed. The cost is the ordering caveat documented on the package —
// a diff for the same session id can already be sitting in the client's
// channel by the time the (slightly stale) snapshot line for it is sent,
// so the client may see that id twice in quick succession. Harmless: SSE
// "session" events are idempotent last-write-wins by id.
func (h *EventsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.authz.ScopeFromBearer(bearerFromRequest(r))
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized: missing or invalid token")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if rc.Flush() != nil {
		return
	}

	c := h.bus.subscribe(scope)
	defer h.bus.unsubscribe(c)

	for _, rec := range h.poller.Snapshot(scope) {
		data, err := json.Marshal(sessionEvent{Type: "session", Record: rec})
		if err != nil {
			continue
		}
		if !writeSSEEvent(w, rc, "session", data) {
			return
		}
	}

	keepalive := time.NewTicker(sseKeepaliveInterval)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-c.ch:
			if !ok {
				// Broadcaster dropped this client (slow-consumer policy): end
				// the stream, same as the caller disconnecting on its own.
				return
			}
			if !writeSSEEvent(w, rc, msg.eventType, msg.data) {
				return
			}
		case <-keepalive.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			if rc.Flush() != nil {
				return
			}
		}
	}
}

// writeSSEEvent writes one `event:`/`data:` frame and flushes it
// immediately, mirroring the dispatcher's own per-write-flush streaming
// discipline (dispatcher.go's httpSink.Write). Returns false on any
// write/flush error — the signal callers use to end the stream, since that
// error means the client went away.
func writeSSEEvent(w io.Writer, rc *http.ResponseController, eventType string, data []byte) bool {
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data); err != nil {
		return false
	}
	return rc.Flush() == nil
}

// bearerFromRequest extracts the caller's bearer token from ?token= (the
// path a browser EventSource uses, since it cannot set request headers) or,
// when that is absent, a standard "Authorization: Bearer <token>" header
// (for any other client). An empty return means no token was supplied by
// either transport.
//
// SECURITY: the ?token= value returned here is the caller's full-privilege
// dispatch JWT riding in a URL query string — see EventsHandler's doc
// comment for the access-log redaction requirement this places on anything
// fronting the route.
func bearerFromRequest(r *http.Request) string {
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	if ah := r.Header.Get("Authorization"); strings.HasPrefix(ah, "Bearer ") {
		return strings.TrimPrefix(ah, "Bearer ")
	}
	return ""
}
