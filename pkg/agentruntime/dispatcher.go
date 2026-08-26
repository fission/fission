// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// dispatcher.go implements Dispatcher, the request-path component that turns
// an inbound "/agents/{namespace}/{name}" turn into a session-scoped call to
// the router-internal listener: it resolves the calling AgentEntry from
// AgentView, mints or validates a session id, bookkeeps the SessionRecord in
// SessionStore, forwards the turn, and streams the function's response back
// incrementally.
package agentruntime

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/endpointcache"
	"github.com/fission/fission/pkg/statestore"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/uuid"
)

// agentTracerName scopes the invoke_agent span's Tracer (see Dispatcher.tracer).
const agentTracerName = "fission/agentruntime"

const (
	// HeaderSession is the response header carrying the resolved session id —
	// the single source of truth for which session a turn landed on, whether
	// the id was supplied by the caller or minted here.
	HeaderSession = fv1.DefaultAgentSessionHeader
	// HeaderYield is the function-response header a turn uses to signal it is
	// done producing output for this session (as opposed to merely finishing
	// one turn while more are expected soon).
	HeaderYield = "X-Fission-Agent-Yield"
	// YieldWaiting is the HeaderYield value that flips a session to idle
	// immediately, rather than waiting out its normal IdleAfter window.
	YieldWaiting = "waiting"
	// YieldContinue is the HeaderYield value that asks the runtime to
	// re-invoke this session asap (self-continuation). Delivered
	// at-least-once via the agent-wake queue.
	YieldContinue = "continue"

	// HeaderTurns carries the session's turn count (Stats.Turns BEFORE this
	// turn) to the function, so stateless fixtures/loops can bound
	// themselves.
	HeaderTurns = "X-Fission-Agent-Turns"
	// HeaderWakeID and HeaderWakeAttempt carry a wake-delivered turn's
	// identity to the function, mirroring asyncinvoke's HeaderInvocationID /
	// HeaderInvocationAttempt replay pattern
	// (pkg/router/asyncinvoke/deliverer.go). Set only when the turn arrived
	// via the wake queue (TurnRequest.WakeID != "").
	HeaderWakeID      = "X-Fission-Agent-Wake-Id"
	HeaderWakeAttempt = "X-Fission-Agent-Wake-Attempt"

	// HeaderParent is the request header a caller may set on a turn that
	// creates a session, naming the spawning parent session in ParentRef's
	// canonical "<namespace>/<agent>/<id>" form. It is consulted ONLY when
	// this turn is the one that creates the session record (DispatchTurn's
	// ErrNotFound branch, via resolveParentage) — Parent/Depth are
	// immutable after Create (session.go), so on resume or on a
	// create-race loss the header is only ever compared against the
	// already-stored value, never applied; a mismatch there is dropped
	// with a V(1) log, never an error, never a write.
	HeaderParent = "X-Fission-Agent-Parent"

	// maxTurnBodyBytes caps a buffered turn request body. The HMAC
	// ServiceSigner binds the signature to a body hash (pkg/auth/hmac/hmac.go),
	// so the request body MUST be fully buffered (and replayable via GetBody)
	// before signing — a streamed client body cannot be signed. 4 MiB,
	// matching the workflow invoker's maxResultBytes; 413 above it.
	maxTurnBodyBytes = 4 << 20

	// streamChunkBytes is the read/write/flush buffer size used to relay the
	// upstream response incrementally, replicating the router's own
	// ReverseProxy FlushInterval=-1 streaming behavior (functionHandler.go).
	streamChunkBytes = 32 << 10
)

// sessionIDPattern bounds a caller-supplied session id: it becomes a
// statestore key and a URL/header value, so it is restricted to a safe,
// portable character set.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// TurnRequest is one session turn, transport-agnostic: everything
// DispatchTurn needs to run a turn, independent of whether it arrived over
// HTTP (the dispatch handler) or via the wake queue's consumer.
type TurnRequest struct {
	Namespace, Agent, SessionID string
	Body                        []byte
	ContentType                 string
	WakeID                      string // non-empty only for wake-delivered turns
	WakeAttempt                 int    // 1-based, wake-delivered turns only
	// ParentHeader is the raw HeaderParent value from the inbound HTTP
	// request, copied in by dispatch() — the only place that sees
	// *http.Request. The wake consumer (wake.go) builds its TurnRequest
	// with no such header, so ParentHeader is always "" there; that zero
	// value is exactly what makes a wake-recreated session fall back to
	// root (no Parent, Depth 0) with NO change to wake.go — see
	// resolveParentage.
	ParentHeader string
}

// TurnResult is the outcome of one DispatchTurn call: the HTTP handler
// surfaces it on the response, the wake consumer switches on it to decide
// Ack/Nack/Kill.
type TurnResult struct {
	StatusCode int
	Yield      string
	// WakeEnqueueAttempted reports whether the shared post-response
	// bookkeeping (step 5 below) decided this turn's continuation was
	// WITHIN the maxContinuations cap — i.e. doEnqueue was true — REGARDLESS
	// of whether an enqueuer was wired (d.wake nil) or its EnqueueWake call
	// itself errored or deduped. It exists so a caller performing its own
	// out-of-band revival enqueue (the wake consumer's reviveChain,
	// wake.go) can tell "the cap allowed a continuation here" from "the cap
	// was already spent" without re-deriving maxContinuations/Continuations
	// bookkeeping itself — Yield alone is NOT enough, since a function past
	// the cap keeps yielding "continue" forever; only this flag reflects the
	// cap decision.
	WakeEnqueueAttempted bool
}

// ShouldReviveChain reports whether this turn's outcome warrants the wake
// consumer's out-of-band chain revival (wake.go's reviveChain): the shared
// path's own maxContinuations cap allowed the continuation
// (WakeEnqueueAttempted) AND the function asked to continue. Gating on
// WakeEnqueueAttempted rather than Yield alone is load-bearing — a function
// past the cap keeps yielding "continue" forever, so a Yield-only gate would
// revive a capped-out chain indefinitely (see reviveChain's doc comment).
func (r TurnResult) ShouldReviveChain() bool {
	return r.WakeEnqueueAttempted && r.Yield == YieldContinue
}

// ErrNotAgent is returned by DispatchTurn when (namespace, agent) is not in
// the AgentView — e.g. the Function was deleted or its Agent block removed
// between a caller resolving it and the turn actually dispatching.
var ErrNotAgent = errors.New("agentruntime: not an agent function")

// ErrStoreUnavailable is returned by DispatchTurn on a quota-bearing turn when
// the session store is unhealthy enough that the MaxSessions budget cannot be
// evaluated (a Get that fails for a reason other than ErrNotFound, or a Create
// that fails for a reason other than ErrSessionQuota/ErrSessionExists). It
// FAILS CLOSED: rather than forwarding an unmetered turn that could silently
// bypass the quota, the turn is rejected. The HTTP handler maps it to 503; the
// wake consumer classifies it as a retry (transient store degradation). It is
// deliberately NOT raised when the agent has no quota (MaxSessions == 0) —
// there is no budget to protect, so the long-standing "bookkeeping never gates
// a turn" rule still applies there.
var ErrStoreUnavailable = errors.New("agentruntime: session store unavailable")

// ErrSpawnDepthExceeded is returned by DispatchTurn when a turn that would
// CREATE a new session names a parent (HeaderParent) whose resolved chain
// depth (parent.Depth+1) exceeds maxSpawnDepth. It is returned by
// resolveParentage BEFORE Create runs — no record is minted for a rejected
// spawn. The HTTP handler (dispatch) maps it to 400; it can never be
// produced by the wake consumer's re-dispatch (TurnRequest.ParentHeader is
// always "" there, so resolveParentage never reaches the cap check).
var ErrSpawnDepthExceeded = errors.New("agentruntime: spawn depth exceeded")

// enqueuer is the continue-chain seam DispatchTurn calls into on
// yield=continue (implemented by WakeService). A Dispatcher's zero
// value has a nil enqueuer, which disables chaining — used by tests that
// construct a Dispatcher without calling SetWakeEnqueuer.
type enqueuer interface {
	EnqueueWake(ctx context.Context, ns, agent, session string, turns int64) error
}

// TurnSink receives the upstream response DispatchTurn streams back. Begin is
// called EXACTLY ONCE, before any body bytes, with the upstream
// status/content-type/yield — values only DispatchTurn sees, since it owns
// the upstream call. Write is called per chunk as DispatchTurn relays the
// upstream body: the HTTP sink flushes after every Write; the wake sink
// ignores Begin and discards the body through a bounded counter.
type TurnSink interface {
	Begin(statusCode int, contentType, yield string)
	io.Writer
}

// httpSink adapts an http.ResponseWriter to TurnSink for the HTTP dispatch
// path. X-Fission-Session is set by dispatch() before DispatchTurn runs (it
// resolves independently of the upstream call and must precede any
// WriteHeader); Begin adds the yield echo and Content-Type and then commits
// WriteHeader(status) — those values are only known once DispatchTurn's
// upstream call returns, so they MUST be set here, before the first Write,
// never by dispatch() itself.
type httpSink struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (s *httpSink) Begin(statusCode int, contentType, yield string) {
	if yield != "" {
		s.w.Header().Set(HeaderYield, yield)
	}
	if contentType != "" {
		s.w.Header().Set("Content-Type", contentType)
	}
	s.w.WriteHeader(statusCode)
}

// Write relays one chunk and flushes immediately, replicating the router's
// own ReverseProxy FlushInterval=-1 streaming behavior (functionHandler.go)
// instead of buffering until DispatchTurn's read loop completes.
func (s *httpSink) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	if err != nil {
		return n, err
	}
	_ = s.rc.Flush()
	return n, nil
}

// Dispatcher serves inbound agent turns.
type Dispatcher struct {
	logger    logr.Logger
	view      *AgentView
	store     *SessionStore
	client    *http.Client // transport pre-wrapped with the HMAC signer
	routerURL string
	retention time.Duration
	now       func() time.Time

	// maxContinuations caps yield=continue self-chaining per session (0 =
	// unlimited). See DispatchTurn's step-5 bookkeeping closure.
	maxContinuations int64

	// maxSpawnDepth caps the depth resolveParentage will admit a spawned
	// session to: parent.Depth+1 <= maxSpawnDepth, checked BEFORE Create.
	// Unlike maxContinuations, 0 is NOT "unlimited" here — 0 means "no
	// spawning", rejecting every parented create. That divergence is
	// deliberate: this cap exists to bound resource exhaustion from an
	// unbounded spawn tree, and "unlimited" is never a safe default for
	// that guard. A root session (no claimed parent, or an unreadable/
	// unparseable claim) is never subject to this cap regardless of its
	// value — see resolveParentage.
	maxSpawnDepth int64

	// wake is the continue-chain seam. It is injected AFTER construction via
	// SetWakeEnqueuer — see that method's doc comment for the
	// construction-cycle rationale and the pre-serve contract. nil disables
	// chaining.
	wake enqueuer

	// index is the best-effort CurrentPod prediction seam, injected AFTER
	// construction via SetEndpointIndex — see that method's
	// doc comment. nil disables prediction: DispatchTurn leaves
	// SessionRecord.CurrentPod untouched rather than clearing it.
	index *endpointcache.Index

	// tracerProvider is the invoke_agent span's TracerProvider seam,
	// injected AFTER construction via SetTracerProvider (mirroring
	// SetWakeEnqueuer/SetEndpointIndex's construction-cycle rationale) —
	// see that method's doc comment. nil means "use the process-wide
	// otel.GetTracerProvider()", which is what fission-bundle's main
	// installs in production (pkg/utils/otel/provider.go); tests thread an
	// explicit sdktrace.NewTracerProvider + tracetest.SpanRecorder instead,
	// since the global default propagator/provider in tests is a no-op that
	// would make span assertions pass vacuously.
	tracerProvider trace.TracerProvider
}

// NewDispatcher returns a Dispatcher. retention is added to an entry's
// IdleAfter+ArchiveAfter to compute the live-key TTL passed to
// SessionStore.Create/Update (orphan self-expiry covers idle + archive-wait +
// archived-copy retention in one live-key lease). maxContinuations caps
// yield=continue self-chaining per session (0 = unlimited); maxSpawnDepth
// caps spawn-chain depth (0 = no spawning, see the field's doc comment); the
// wake enqueuer itself is wired separately via SetWakeEnqueuer.
func NewDispatcher(logger logr.Logger, view *AgentView, store *SessionStore, client *http.Client, routerURL string, retention time.Duration, now func() time.Time, maxContinuations int64, maxSpawnDepth int64) *Dispatcher {
	return &Dispatcher{
		logger:           logger,
		view:             view,
		store:            store,
		client:           client,
		routerURL:        routerURL,
		retention:        retention,
		now:              now,
		maxContinuations: maxContinuations,
		maxSpawnDepth:    maxSpawnDepth,
	}
}

// SetWakeEnqueuer wires the continue-chain seam post-construction. This
// breaks a construction cycle: WakeService needs the Dispatcher (to call
// DispatchTurn), so the Dispatcher cannot take the enqueuer as a
// NewDispatcher param without one of them being unbuildable. Start's
// sequencing is: build Dispatcher, build WakeService(dispatcher), then
// dispatcher.SetWakeEnqueuer(wakeService), all before the manager starts
// serving — so this MUST be called before the dispatcher serves any turn. It
// is a plain field, not synchronized; that pre-serve ordering is what makes
// an unsynchronized write to it safe.
func (d *Dispatcher) SetWakeEnqueuer(w enqueuer) {
	d.wake = w
}

// SetEndpointIndex wires the best-effort CurrentPod prediction seam
// post-construction, mirroring SetWakeEnqueuer's construction-cycle
// rationale: main.go's Start builds the endpointcache.Index only after the
// Manager (and the RBAC preflight deciding whether an informer ever feeds it)
// is set up, well after the Dispatcher itself is constructed. Like
// SetWakeEnqueuer this is a plain, unsynchronized field write and MUST be
// called before the dispatcher serves any turn — that pre-serve ordering is
// what makes the unsynchronized write safe. ix may be nil (prediction stays
// disabled) or an Index with no informer feeding it (permanently empty
// Lookup results — the RBAC-degraded case) — DispatchTurn treats both
// identically: CurrentPod is left untouched, never cleared.
func (d *Dispatcher) SetEndpointIndex(ix *endpointcache.Index) {
	d.index = ix
}

// SetTracerProvider wires the invoke_agent span's TracerProvider seam
// post-construction, mirroring SetWakeEnqueuer/SetEndpointIndex's plain,
// unsynchronized pre-serve field-write contract — call it before the
// dispatcher serves any turn. Production leaves it unset (nil), which makes
// tracer() fall back to the process-wide otel.GetTracerProvider(); unit
// tests set an explicit provider (e.g. sdktrace.NewTracerProvider with a
// tracetest.SpanRecorder syncer) so span assertions are hermetic and do not
// depend on — or pollute — global otel state.
func (d *Dispatcher) SetTracerProvider(tp trace.TracerProvider) {
	d.tracerProvider = tp
}

// tracer returns the Tracer DispatchTurn starts its invoke_agent span from.
//
// GenAI semantic conventions are PRE-STABLE (experimental) as of this pin:
// go.opentelemetry.io/otel/semconv/v1.37.0 supplies the gen_ai.* attribute
// keys used below (GenAIOperationNameInvokeAgent = "invoke_agent",
// GenAIAgentName = "gen_ai.agent.name"). Re-verify attribute names against
// the semconv package on any future bump — the spec has moved these before.
func (d *Dispatcher) tracer() trace.Tracer {
	tp := d.tracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return tp.Tracer(agentTracerName)
}

// Handler serves POST /agents/{namespace}/{name}; other methods get 405 (the
// Go 1.22+ ServeMux does this automatically for a method-qualified pattern).
func (d *Dispatcher) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /agents/{namespace}/{name}", d.dispatch)
	return mux
}

// dispatch keeps session-id resolution/minting/validation, the request body
// read + 413 check, and error-status mapping — the parts that are either
// HTTP-specific or must run before any session record write — and delegates
// everything else to DispatchTurn.
func (d *Dispatcher) dispatch(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("namespace")
	name := r.PathValue("name")

	// Step 1: resolve the agent entry. This lookup is ALSO repeated inside
	// DispatchTurn (which has no AgentEntry in TurnRequest, since the wake
	// path has no prior lookup to reuse) — it is needed here too because
	// session id resolution below depends on entry.SessionSource/SessionName.
	// A benign double-lookup of the same in-memory map; on the rare race
	// where the entry vanishes between the two, DispatchTurn's ErrNotAgent
	// maps to the same 404 below.
	entry, ok := d.view.Lookup(ns, name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not an agent function")
		return
	}

	// Step 2: resolve (or reject, or mint) the session id. The response
	// header is set as soon as the id resolves — it must precede any
	// WriteHeader call below.
	sessionID, ok := resolveSessionID(r, entry)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid session id")
		return
	}
	w.Header().Set(HeaderSession, sessionID)

	// Read + validate the body BEFORE calling DispatchTurn: an oversized or
	// unreadable request must never create or refresh a session (a bad
	// request is a client-side problem, so a read failure here is a 400, not
	// a 502 — nothing upstream has been touched yet).
	body, err := io.ReadAll(io.LimitReader(r.Body, maxTurnBodyBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "reading request body")
		return
	}
	if len(body) > maxTurnBodyBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	tr := TurnRequest{
		Namespace:    ns,
		Agent:        name,
		SessionID:    sessionID,
		Body:         body,
		ContentType:  r.Header.Get("Content-Type"),
		ParentHeader: r.Header.Get(HeaderParent),
	}
	sink := &httpSink{w: w, rc: http.NewResponseController(w)}
	// DispatchTurn's errors arrive BEFORE sink.Begin (no status has been
	// committed yet), so the handler can still write an error status here.
	if _, err := d.DispatchTurn(r.Context(), tr, sink); err != nil {
		switch {
		case errors.Is(err, ErrSessionQuota):
			writeErr(w, http.StatusTooManyRequests, "session quota exceeded")
		case errors.Is(err, ErrStoreUnavailable):
			writeErr(w, http.StatusServiceUnavailable, "session store unavailable")
		case errors.Is(err, ErrNotAgent):
			writeErr(w, http.StatusNotFound, "not an agent function")
		case errors.Is(err, ErrSpawnDepthExceeded):
			writeErr(w, http.StatusBadRequest, "spawn depth exceeded")
		default:
			writeErr(w, http.StatusBadGateway, "calling upstream function")
		}
	}
}

// DispatchTurn runs one turn: view lookup, record get/create (+quota),
// signed forward to the router internal listener streaming the response into
// sink (Begin first, then body chunks), then the shared bookkeeping (meters,
// yield, LastActiveAt, and continue-enqueue). The HTTP handler and the wake
// consumer both call this — chaining lives HERE so both turn kinds chain.
func (d *Dispatcher) DispatchTurn(ctx context.Context, tr TurnRequest, sink TurnSink) (TurnResult, error) {
	turnSource := "http"
	if tr.WakeID != "" {
		turnSource = "wake"
	}
	// The invoke_agent span MUST start here — before EVERY early return in
	// this function (including the entry-lookup miss immediately below) and,
	// above all, before bgCtx is derived a few lines down: context.
	// WithoutCancel preserves context VALUES (just not cancellation), and
	// EnqueueWake is later called with bgCtx (step 5, below) — a span
	// started any later would silently degrade every yield=continue
	// continuation to a root span, masked by the wake envelope's
	// backward-compat Trace-absent fallback (wake.go). `defer span.End()`
	// immediately after Start covers every return in this function by
	// construction, and — since it runs as part of a normal function
	// return — always fires AFTER the step-5 settle below, satisfying "end
	// after settle" with no separate End() call anywhere else. Never end or
	// re-parent this span from a bgCtx-derived code path: carrying its
	// values there via bgCtx is exactly what the wake stitch needs.
	//
	// An HTTP turn starts from the caller's ctx, which for the dispatch mux
	// is already a child of the "fission-agentruntime" server span
	// (main.go's otelUtils.GetHandlerWithOTEL wrap). A wake-delivered turn
	// starts from the delivery ctx wake.go's process() extracted from the
	// wake envelope's optional Trace field, making the continuation's span
	// a child in the SAME trace as the turn that originally enqueued it —
	// or, when Trace is absent (no propagator injected anything, or a
	// pre-observability envelope), a fresh root span, never an error.
	ctx, span := d.tracer().Start(ctx, "invoke_agent "+tr.Agent, trace.WithAttributes(
		semconv.GenAIOperationNameInvokeAgent,
		semconv.GenAIAgentName(tr.Agent),
		attribute.String("fission.agent.namespace", tr.Namespace),
		attribute.String("fission.session.id", tr.SessionID),
		attribute.String("fission.turn.source", turnSource),
	))
	defer span.End()

	entry, ok := d.view.Lookup(tr.Namespace, tr.Agent)
	if !ok {
		return TurnResult{}, ErrNotAgent
	}

	// bgCtx carries no cancellation from the caller: the post-response
	// bookkeeping (and the transport-failure bookkeeping below) must still
	// land even if the caller has already gone away, since it is what keeps
	// Stats/Status honest and self-heals nothing on its own. It DOES carry
	// every value on ctx — including the invoke_agent span started above —
	// which is exactly what lets EnqueueWake (step 5) inject this turn's
	// trace into a continuation's wake envelope.
	bgCtx := context.WithoutCancel(ctx)
	liveTTL := entry.IdleAfter + entry.ArchiveAfter + d.retention
	now := d.now()

	// Step 3: bookkeep the session record. A registry write is never a
	// request gate, EXCEPT session quota on Create. turnsAtStart captures the
	// record's Stats.Turns AT READ TIME, echoed to the function via
	// HeaderTurns below.
	var turnsAtStart int64
	// quotaBearing is true when this agent enforces a live-session budget. A
	// store error on a quota-bearing turn must FAIL CLOSED (ErrStoreUnavailable)
	// rather than forward an unmetered turn that could silently overrun the
	// budget; a non-quota agent keeps the "bookkeeping never gates a turn" rule.
	quotaBearing := entry.MaxSessions > 0
	rec, version, err := d.store.Get(ctx, tr.Namespace, tr.Agent, tr.SessionID)
	switch {
	case errors.Is(err, statestore.ErrNotFound):
		parent, depth, perr := d.resolveParentage(ctx, tr.Namespace, tr.Agent, tr.ParentHeader)
		if perr != nil {
			return TurnResult{}, perr
		}
		newRec := SessionRecord{
			ID:           tr.SessionID,
			Agent:        tr.Agent,
			Namespace:    tr.Namespace,
			Status:       StatusActive,
			CreatedAt:    now,
			LastActiveAt: now,
			Parent:       parent,
			Depth:        depth,
		}
		switch cerr := d.store.Create(ctx, newRec, entry.MaxSessions, liveTTL); {
		case cerr == nil:
			// created; turnsAtStart is 0 (SessionStats zero value).
		case errors.Is(cerr, ErrSessionQuota):
			return TurnResult{}, ErrSessionQuota
		case errors.Is(cerr, ErrSessionExists):
			// Another replica (or a concurrent turn) created the same id
			// between our Get miss and this Create — a lost race, not a
			// failure. Fall through to the normal activation path instead of
			// silently dropping this turn's bookkeeping.
			d.logger.Info("session created concurrently, activating existing record", "namespace", tr.Namespace, "agent", tr.Agent, "session", tr.SessionID)
			d.casUpdateFresh(ctx, tr.Namespace, tr.Agent, tr.SessionID, liveTTL, func(rec *SessionRecord) {
				turnsAtStart = rec.Stats.Turns
				// This turn lost the create race: the winner's Create already
				// fixed Parent/Depth immutably. Our own ParentHeader (if any)
				// names a claim that was never applied — only compared, for
				// diagnostics.
				d.checkParentHeaderIgnored(tr.Namespace, tr.Agent, tr.SessionID, tr.ParentHeader, rec)
				rec.Status = StatusActive
				rec.LastActiveAt = now
			})
		default:
			// The Create failed for a reason that is neither the quota nor a
			// lost create-race: the store is unhealthy. On a quota-bearing
			// agent, forwarding now would mint an unmetered session that
			// bypasses MaxSessions, so fail closed instead.
			d.logger.Error(cerr, "creating session record", "namespace", tr.Namespace, "agent", tr.Agent, "session", tr.SessionID)
			if quotaBearing {
				return TurnResult{}, ErrStoreUnavailable
			}
		}
	case err != nil:
		// A Get that failed for a reason other than ErrNotFound leaves us unable
		// to tell whether this session already exists (and is already counted)
		// or would be a new one — so on a quota-bearing agent we cannot safely
		// forward without risking an unmetered session; fail closed.
		d.logger.Error(err, "getting session record", "namespace", tr.Namespace, "agent", tr.Agent, "session", tr.SessionID)
		if quotaBearing {
			return TurnResult{}, ErrStoreUnavailable
		}
	default:
		turnsAtStart = rec.Stats.Turns
		// A resumed session's Parent/Depth are already fixed at Create; a
		// ParentHeader on THIS turn (if any) is only ever compared against
		// them, never applied.
		d.checkParentHeaderIgnored(tr.Namespace, tr.Agent, tr.SessionID, tr.ParentHeader, &rec)
		d.casUpdate(ctx, tr.Namespace, tr.Agent, tr.SessionID, rec, version, liveTTL, func(rec *SessionRecord) {
			rec.Status = StatusActive
			rec.LastActiveAt = now
		})
	}
	span.SetAttributes(attribute.Int64("fission.turn", turnsAtStart))

	// Step 4: forward the turn.
	upstream := d.routerURL + utils.UrlForFunction(tr.Agent, tr.Namespace)
	if entry.SessionSource == fv1.SessionSourceQueryParam {
		upstream += "?" + url.Values{entry.SessionName: {tr.SessionID}}.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, bytes.NewReader(tr.Body))
	if err != nil {
		return TurnResult{}, fmt.Errorf("building upstream request: %w", err)
	}
	if tr.ContentType != "" {
		req.Header.Set("Content-Type", tr.ContentType)
	}
	req.Header.Set(HeaderSession, tr.SessionID)
	if entry.SessionSource == fv1.SessionSourceHeader {
		req.Header.Set(entry.SessionName, tr.SessionID)
	}
	req.Header.Set(HeaderTurns, strconv.FormatInt(turnsAtStart, 10))
	if tr.WakeID != "" {
		req.Header.Set(HeaderWakeID, tr.WakeID)
		req.Header.Set(HeaderWakeAttempt, strconv.Itoa(tr.WakeAttempt))
	}

	start := d.now()
	resp, err := d.client.Do(req)
	if err != nil {
		// A client disconnect surfaces here as context.Canceled: that is not a
		// function error, so it is logged at V(1) and must NOT inflate
		// Stats.Errors (mirrors the sweeper's own Canceled special-casing). A
		// DeadlineExceeded or a real transport failure IS a function-observable
		// error and is metered as one.
		canceled := errors.Is(err, context.Canceled)
		if canceled {
			d.logger.V(1).Info("upstream call canceled by caller disconnect", "namespace", tr.Namespace, "agent", tr.Agent, "session", tr.SessionID)
		} else {
			d.logger.Error(err, "calling upstream function", "namespace", tr.Namespace, "agent", tr.Agent, "session", tr.SessionID)
		}
		// A failed turn is still a turn: meter it using bgCtx so the write is
		// not lost if the caller has already gone. Errors is bumped only for a
		// genuine function/transport failure, never a caller disconnect.
		d.casUpdateFresh(bgCtx, tr.Namespace, tr.Agent, tr.SessionID, liveTTL, func(rec *SessionRecord) {
			rec.Stats.Turns++
			if !canceled {
				rec.Stats.Errors++
			}
			rec.Status = StatusActive
			rec.LastActiveAt = d.now()
		})
		// Span status: Error on a transport failure (this branch), Ok on a
		// normal completion (the final return below) — the two outcomes the
		// plan pins explicitly. A caller-disconnect (context.Canceled) is
		// still a span-level Error: that distinction only matters for
		// Stats.Errors metering above, not for whether this span's own
		// operation completed successfully.
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return TurnResult{}, fmt.Errorf("calling upstream function: %w", err)
	}
	defer resp.Body.Close()

	// The yield outcome is surfaced to the caller (not just consumed for our
	// own bookkeeping below) so callers can observe done-vs-waiting-vs-continue.
	yield := resp.Header.Get(HeaderYield)
	sink.Begin(resp.StatusCode, resp.Header.Get("Content-Type"), yield)
	streamToSink(sink, resp.Body)
	elapsed := d.now().Sub(start)

	// Best-effort CurrentPod prediction: the ready endpoint PredictSticky
	// picks for this session's sticky key right now —
	// PREDICTED, matching the router's own sticky pick while the pod set is
	// stable, UI truth only, never routing truth (see PredictSticky's doc
	// comment). Computed ONCE here, outside the mutate closure below: mutate
	// is pure and casUpdateFresh may invoke it twice on a version-conflict
	// retry, and PredictSticky is pure too, so a second call would only be
	// wasted work, never a correctness issue — but there is no reason to pay
	// it twice. predictedPod stays "" when d.index is nil or no ready
	// endpoint exists yet; the mutate closure below only overwrites
	// rec.CurrentPod when it is non-empty, so a transient prediction miss
	// never blanks a previously-known pod.
	var predictedPod string
	if d.index != nil {
		if ep, ok := endpointcache.PredictSticky(d.index.Lookup(tr.Namespace, tr.Agent, ""), tr.SessionID); ok {
			predictedPod = ep.Address
		}
	}

	// Step 5: post-response bookkeeping. Reads a fresh record — never the
	// version held from step 3 — because the upstream call above may have
	// taken long enough for another turn to have CAS-written the record
	// underneath it. Uses bgCtx so a client disconnect during/after the
	// stream does not lose this turn's meter.
	//
	// doEnqueue/enqueueTurns are decided inside the mutate closure (which
	// sees the actually-persisted record) but the enqueuer call itself
	// happens OUTSIDE it: mutate is a pure record mutation that casUpdate may
	// invoke twice (once, then again on a version-conflict retry with a
	// freshly re-read record), and an external call has no business running
	// twice just because a CAS lost a race. Both vars are reset on every
	// invocation so only the final (persisted) decision survives a retry.
	var doEnqueue bool
	var enqueueTurns int64
	persisted := d.casUpdateFresh(bgCtx, tr.Namespace, tr.Agent, tr.SessionID, liveTTL, func(rec *SessionRecord) {
		doEnqueue = false
		enqueueTurns = 0

		rec.Stats.Turns++
		rec.Stats.ActiveMillis += elapsed.Milliseconds()
		if resp.StatusCode >= http.StatusInternalServerError {
			rec.Stats.Errors++
		}
		if predictedPod != "" {
			rec.CurrentPod = predictedPod
		}
		// LastActiveAt refreshes on EVERY branch: a waiting->idle or
		// continue turn still just happened just now, it is only the
		// session's next-turn clock (IdleAfter/ArchiveAfter) that starts
		// running from it.
		rec.LastActiveAt = d.now()
		switch yield {
		case YieldWaiting:
			rec.Status = StatusIdle
		case YieldContinue:
			rec.Status = StatusActive
			rec.Stats.Continuations++
			switch {
			case d.maxContinuations == 0 || rec.Stats.Continuations <= d.maxContinuations:
				doEnqueue = true
				enqueueTurns = rec.Stats.Turns
			case rec.Stats.Continuations == d.maxContinuations+1:
				// Log exactly once per session in the common (no CAS
				// conflict) case: Continuations only increases, so this
				// equality holds on exactly the turn that first breaches the
				// cap. A rare conflict-retry race could hit this branch
				// twice (first attempt and the retry both landing on
				// cap+1) — not worth extra state to dedupe further.
				d.logger.Info("continuation cap reached, not enqueueing wake", "namespace", tr.Namespace, "agent", tr.Agent, "session", tr.SessionID, "maxContinuations", d.maxContinuations)
			}
		default:
			rec.Status = StatusActive
		}
	})
	// If neither CAS write landed (both attempts lost, or the fresh Get
	// failed), the enqueue decision above rode on a count that was never
	// persisted — a phantom. Suppress the enqueue so a lost write can never
	// drive a wake keyed on a turn count the store does not hold.
	if !persisted {
		doEnqueue = false
		enqueueTurns = 0
	}
	// Bookkeeping never gates a turn: an enqueue failure is logged, never
	// returned to the caller.
	if doEnqueue && d.wake != nil {
		if err := d.wake.EnqueueWake(bgCtx, tr.Namespace, tr.Agent, tr.SessionID, enqueueTurns); err != nil {
			d.logger.Error(err, "enqueueing continuation wake", "namespace", tr.Namespace, "agent", tr.Agent, "session", tr.SessionID)
		}
	}

	span.SetAttributes(attribute.String("fission.yield", yield))
	span.SetStatus(codes.Ok, "")
	return TurnResult{StatusCode: resp.StatusCode, Yield: yield, WakeEnqueueAttempted: doEnqueue}, nil
}

// resolveParentage resolves the Parent/Depth to mint for a session-CREATING
// turn from its raw ParentHeader. It is called ONLY from DispatchTurn's
// ErrNotFound (create) branch — see HeaderParent's doc comment for why every
// other branch only ever compares, via checkParentHeaderIgnored. Depth is
// NEVER taken off the wire: it is always parent.Depth+1, read from the
// claimed parent's OWN stored record, so a forged or stale header can at
// worst misattribute lineage, never fabricate a depth that skips the cap.
//
// Cross-AGENT parentage within one namespace is the supported shape
// (Handoff/multi-agent-orchestration). Cross-NAMESPACE parentage is
// rejected: the dispatcher's store handle is unscoped, so resolving a
// foreign-namespace ref would Get another tenant's record on the strength
// of a caller-supplied header alone, and the minted child's depth (readable
// back by the caller in its own namespace) plus the depth-cap's 400-vs-200
// split would together leak that tenant's session existence and exact spawn
// depth — a cross-tenant oracle. This is a security ruling (RFC amendment
// recorded separately), not a functional gap: nothing on this branch uses
// cross-namespace parentage.
//
// Resolution:
//   - header == "": root (nil parent, depth 0) — the common case (no spawn).
//   - unparseable header: root, logged at V(1) (the header is caller input,
//     not a store-health signal, so this is not worth Error level).
//   - parseable header naming a DIFFERENT namespace than ns: root, logged at
//     V(1) — treated identically to an unparseable header (same fields,
//     same level) so the response is byte-indistinguishable regardless of
//     whether the foreign session exists.
//   - parseable, same-namespace header: Get the claimed parent from the
//     LIVE keyspace; on ANY error (not just ErrNotFound — see the note
//     below) fall back to GetArchived. Found in either: depth =
//     parent.Depth + 1. If int64(depth) > maxSpawnDepth, returns
//     ErrSpawnDepthExceeded — the caller MUST NOT proceed to Create; no
//     record is minted for a rejected spawn. Unreadable in BOTH keyspaces:
//     logged "parent claim dropped" (with both Get errors), admitted as
//     root.
//
// Depth-cap nuance: falling back to root on ANY parent-Get error — not just
// ErrNotFound — means a transient store hiccup fragments what should have
// been one long lineage into a fresh, separately-capped chain rather than
// extending the existing one. That is benign for the resource-exhaustion
// case this cap actually guards: a live runaway spawn loop is, by
// construction, still actively reading and writing its own parent chain, so
// its records stay readable and the cap still bites it. The fragmentation
// risk only lands on the rare, already-cold case of a legitimate deep chain
// whose parent record was transiently unreadable at exactly the wrong
// moment — annoying, never a resource-exhaustion hole.
func (d *Dispatcher) resolveParentage(ctx context.Context, ns, agent, header string) (*ParentRef, int, error) {
	if header == "" {
		return nil, 0, nil
	}
	parent, ok := ParseParentRef(header)
	if !ok {
		d.logger.V(1).Info("unparseable parent header, admitting as root", "namespace", ns, "agent", agent, "headerLen", len(header))
		return nil, 0, nil
	}
	if parent.Namespace != ns {
		// Tenant isolation: never Get a foreign namespace's record on the
		// strength of a caller-supplied header. Logged and admitted exactly
		// like the unparseable-header case above, on purpose — a foreign-ns
		// claim must be indistinguishable from an unparseable one to the
		// caller (see the doc comment above).
		d.logger.V(1).Info("cross-namespace parent header, admitting as root", "namespace", ns, "agent", agent, "headerLen", len(header))
		return nil, 0, nil
	}

	rec, _, liveErr := d.store.Get(ctx, parent.Namespace, parent.Agent, parent.ID)
	err := liveErr
	if err != nil {
		rec, _, err = d.store.GetArchived(ctx, parent.Namespace, parent.Agent, parent.ID)
	}
	if err != nil {
		// Both Get errors are attached (when they differ) so a store outage
		// that silently fragments a legitimate lineage is diagnosable after
		// the fact, instead of looking identical to a plain not-found.
		if errors.Is(liveErr, err) {
			d.logger.V(1).Info("parent claim dropped, admitting as root", "namespace", ns, "agent", agent, "parent", parent.String(), "err", err)
		} else {
			d.logger.V(1).Info("parent claim dropped, admitting as root", "namespace", ns, "agent", agent, "parent", parent.String(), "liveErr", liveErr, "archivedErr", err)
		}
		return nil, 0, nil
	}

	depth := rec.Depth + 1
	if int64(depth) > d.maxSpawnDepth {
		return nil, 0, ErrSpawnDepthExceeded
	}
	return &parent, depth, nil
}

// checkParentHeaderIgnored logs (V(1), never an error, never a write) when
// header is present and disagrees with rec's already-stored parentage. It is
// called from every DispatchTurn branch EXCEPT the creation path: the record
// already exists there, so Parent/Depth (immutable after Create) can only be
// compared against a claim on this turn, never applied. Comparing the raw
// header string against ParentRef.String() (rather than parsing header
// first) is deliberate and still correct: ParentRef.String() is exactly
// ParseParentRef's canonical form, so it also catches an unparseable header
// as a mismatch, with no separate parse-and-branch needed here.
func (d *Dispatcher) checkParentHeaderIgnored(ns, agent, session, header string, rec *SessionRecord) {
	if header == "" {
		return
	}
	stored := ""
	if rec.Parent != nil {
		stored = rec.Parent.String()
	}
	if header != stored {
		d.logger.V(1).Info("parent header ignored: turn did not create this session", "namespace", ns, "agent", agent, "session", session, "stored", stored)
	}
}

// resolveSessionID extracts the session id from r per entry's configured
// source, minting one when absent. It reports false when a caller-supplied
// id fails sessionIDPattern, or starts with CheckpointKeyPrefix — that
// prefix is reserved for the checkpoint KV key within the same function-state
// keyspace (history.go), and sessionIDPattern's charset otherwise permits it
// (dots are legal), so a caller-chosen id could collide with and clobber a
// checkpoint key without this check.
func resolveSessionID(r *http.Request, entry AgentEntry) (string, bool) {
	var raw string
	if entry.SessionSource == fv1.SessionSourceQueryParam {
		raw = r.URL.Query().Get(entry.SessionName)
	} else {
		raw = r.Header.Get(entry.SessionName)
	}
	if raw == "" {
		return uuid.NewString(), true
	}
	if !sessionIDPattern.MatchString(raw) || strings.HasPrefix(raw, CheckpointKeyPrefix) {
		return "", false
	}
	return raw, true
}

// streamToSink relays src to sink incrementally: a read loop with a
// streamChunkBytes buffer, calling sink.Write per chunk. Plain io.Copy
// buffers until completion; this replicates the router's own ReverseProxy
// FlushInterval=-1 streaming behavior instead (the HTTP sink flushes inside
// its own Write; the wake sink just counts bytes). A sink Write error (e.g.
// an HTTP client disconnect) stops the copy but is not otherwise treated as a
// failure — status is already committed via Begin, so there is nothing left
// to do but let the caller's bookkeeping still run.
func streamToSink(sink TurnSink, src io.Reader) {
	buf := make([]byte, streamChunkBytes)
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := sink.Write(buf[:n]); werr != nil {
				return
			}
		}
		if rerr != nil {
			return
		}
	}
}

// casUpdate mutates rec via mutate and CAS-writes it at version with
// liveTTL. On statestore.ErrVersionConflict it retries exactly once with a
// freshly fetched record and version — never the stale one. Any other
// failure, or a second conflict, is logged and swallowed: registry writes are
// bookkeeping, never a request gate. It returns true only when a write
// actually landed, so a caller whose post-write action depends on the mutated
// state having been persisted (the step-5 continue-enqueue) can tell a real
// persist from a phantom one that never reached the store.
//
// Package-level (not a Dispatcher method) so the session workspace routes
// (workspace.go, a later slice) can settle their ArtifactCount/ArtifactBytes
// counters through the exact same retry-once-on-conflict logic without a
// second, drifting copy — Dispatcher's own casUpdate/casUpdateFresh methods
// below are thin delegates to these, byte-identical in behavior.
func casUpdate(ctx context.Context, logger logr.Logger, store *SessionStore, ns, agent, id string, rec SessionRecord, version int64, liveTTL time.Duration, mutate func(*SessionRecord)) bool {
	mutate(&rec)
	err := store.Update(ctx, rec, version, liveTTL)
	if err == nil {
		return true
	}
	if !errors.Is(err, statestore.ErrVersionConflict) {
		logger.Error(err, "updating session record", "namespace", ns, "agent", agent, "session", id)
		return false
	}

	fresh, freshVersion, gerr := store.Get(ctx, ns, agent, id)
	if gerr != nil {
		logger.Error(gerr, "re-getting session record after conflict", "namespace", ns, "agent", agent, "session", id)
		return false
	}
	mutate(&fresh)
	if uerr := store.Update(ctx, fresh, freshVersion, liveTTL); uerr != nil {
		logger.Error(uerr, "updating session record after retry", "namespace", ns, "agent", agent, "session", id)
		return false
	}
	return true
}

// casUpdateFresh re-Gets the record immediately before the CAS write, then
// delegates to casUpdate. Used whenever an unbounded amount of work (an
// upstream call) may have happened since any previously held version was
// read. It returns casUpdate's persisted flag (false also when the fresh Get
// itself fails). Package-level for the same reason casUpdate is — see its
// doc comment.
func casUpdateFresh(ctx context.Context, logger logr.Logger, store *SessionStore, ns, agent, id string, liveTTL time.Duration, mutate func(*SessionRecord)) bool {
	rec, version, err := store.Get(ctx, ns, agent, id)
	if err != nil {
		logger.Error(err, "getting session record for update", "namespace", ns, "agent", agent, "session", id)
		return false
	}
	return casUpdate(ctx, logger, store, ns, agent, id, rec, version, liveTTL, mutate)
}

// casUpdate is Dispatcher's delegate to the package-level casUpdate, over
// this Dispatcher's own logger and store.
func (d *Dispatcher) casUpdate(ctx context.Context, ns, agent, id string, rec SessionRecord, version int64, liveTTL time.Duration, mutate func(*SessionRecord)) bool {
	return casUpdate(ctx, d.logger, d.store, ns, agent, id, rec, version, liveTTL, mutate)
}

// casUpdateFresh is Dispatcher's delegate to the package-level
// casUpdateFresh, over this Dispatcher's own logger and store.
func (d *Dispatcher) casUpdateFresh(ctx context.Context, ns, agent, id string, liveTTL time.Duration, mutate func(*SessionRecord)) bool {
	return casUpdateFresh(ctx, d.logger, d.store, ns, agent, id, liveTTL, mutate)
}

// writeErr answers with a small {"error": msg} JSON object at status.
func writeErr(w http.ResponseWriter, status int, msg string) {
	b, err := json.Marshal(map[string]string{"error": msg})
	if err != nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
