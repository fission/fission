// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package endpointcache holds the router's EndpointSlice-fed endpoint index
// (RFC-0002): a sharded, read-mostly map from function to its ready endpoints,
// maintained from slice informer events and read on the proxy hot path with
// only a shard RLock plus an atomic snapshot load (no allocation, no exclusive
// lock). It is the warm-path address source when the cache mode is on.
package endpointcache

import (
	"fmt"
	"hash/fnv"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

const shardCount = 64

// DefaultQuarantineTTL bounds how long a dial-failed endpoint stays
// unadmissible when NO slice event arrives to clear it. Slice events stay the
// primary (fast) lift; the TTL is the backstop for a steady cluster — without
// it, one transient dial error against a function's only endpoint while the
// executor is down (no slice events, no pod churn) pins the function to the
// executor fallback until the executor returns, which is exactly the outage
// mode the warm path exists to survive. After expiry the endpoint is re-tried;
// a genuinely dead pod just gets re-quarantined by the next dial failure.
const DefaultQuarantineTTL = 30 * time.Second

// dialTimeoutStrikeLimit is how many dial timeouts an address absorbs within
// one TTL window before it is quarantined. A dial timeout is how a
// saturated-but-alive pod presents (SYN backlog full), so a single strike must
// not remove a function's only endpoint — that turns saturation into an
// executor-fallback specialization storm. A truly dead-but-Ready pod still
// quarantines after this many failed dials; connection refusals (port closed,
// pod gone) skip strikes and quarantine immediately.
const dialTimeoutStrikeLimit = 3

// dialStrike is one address's soft-failure record; count resets when the
// window lapses.
type dialStrike struct {
	count  int
	expiry time.Time
}

type (
	// FnKey identifies a function by its CR coordinates plus, when present,
	// the RFC-0025 published version it was specialized for (the labels
	// mirrored from the function's Service onto its slices). Version is ""
	// for the unversioned (live-spec) pool -- today's only pool, until phase
	// 3 starts stamping fission.io/function-version on Function objects --
	// so two versions of one function occupy distinct FnKeys and therefore
	// distinct, independently addressable warm pools.
	FnKey struct {
		Namespace string
		Name      string
		Version   string
	}

	// Endpoint is one slice endpoint, pre-resolved to a dialable address.
	Endpoint struct {
		// Address is host:port, the same form the executor returns for poolmgr
		// (podIP:8888).
		Address string
		// URL is the pre-parsed http URL for Address, built once per slice
		// event instead of per request. Callers must treat it as immutable.
		URL *url.URL
		// PodUID keys per-endpoint accounting across index rebuilds.
		PodUID types.UID
		// Ready mirrors the slice endpoint's ready condition.
		Ready bool
		// inflight is the pod's shared in-flight counter (router-local
		// admission accounting; see Admit). Shared with the entry's counters
		// map so it survives rebuilds.
		inflight *atomic.Int64
	}

	// Index is the sharded endpoint index. Writes (slice events) rebuild one
	// function's endpoint list copy-on-write; reads are a shard RLock plus an
	// atomic pointer load.
	Index struct {
		shards [shardCount]indexShard
		// size tracks the number of functions with at least one slice.
		size atomic.Int64
		// quarantineTTL is DefaultQuarantineTTL unless narrowed in tests.
		quarantineTTL time.Duration
	}

	indexShard struct {
		mu sync.RWMutex
		m  map[FnKey]*fnEntry
	}

	fnEntry struct {
		// mu serializes slice-event rebuilds for this function only.
		mu sync.Mutex
		// slices holds each owning slice's endpoints, keyed by slice
		// namespace/name, so per-slice add/update/delete events merge without
		// a lister query.
		slices map[string][]Endpoint
		// counters holds the per-pod in-flight counters, keyed by pod UID so
		// they survive slice-event rebuilds; pruned with the endpoints.
		counters map[types.UID]*atomic.Int64
		// quarantined maps addresses the transport reported dial failures for
		// to their quarantine expiry; skipped by Admit until the next slice
		// event for this function clears them (the slice controller removes
		// dead pods promptly, independent of executor health) or the TTL
		// expires (the backstop when no slice event will come — see
		// DefaultQuarantineTTL). Copy-on-write (like eps) so Admit reads it
		// lock-free; writes happen under mu.
		quarantined atomic.Pointer[map[string]time.Time]
		// strikes counts soft dial failures (timeouts) per address, escalated
		// to a quarantine at dialTimeoutStrikeLimit. Only touched on failure
		// reports (never by Admit), so a plain mu-guarded map suffices;
		// cleared alongside quarantined on slice events.
		strikes map[string]dialStrike
		// eps is the merged endpoint list, swapped copy-on-write. Hot-path
		// readers load it without taking mu.
		eps atomic.Pointer[[]Endpoint]
	}
)

// NewIndex returns an empty endpoint index.
func NewIndex() *Index {
	ix := &Index{quarantineTTL: DefaultQuarantineTTL}
	for i := range ix.shards {
		ix.shards[i].m = make(map[FnKey]*fnEntry)
	}
	return ix
}

func (ix *Index) shard(key FnKey) *indexShard {
	// Inline FNV-1a over the key strings: hash.Hash32 via fnv.New32a would
	// heap-allocate on every call of this per-request path.
	h := uint32(2166136261)
	for i := 0; i < len(key.Namespace); i++ {
		h = (h ^ uint32(key.Namespace[i])) * 16777619
	}
	h = (h ^ uint32('/')) * 16777619
	for i := 0; i < len(key.Name); i++ {
		h = (h ^ uint32(key.Name[i])) * 16777619
	}
	h = (h ^ uint32('/')) * 16777619
	for i := 0; i < len(key.Version); i++ {
		h = (h ^ uint32(key.Version[i])) * 16777619
	}
	return &ix.shards[h%shardCount]
}

// Lookup returns the function's merged endpoint list (nil when unknown). The
// returned slice is immutable — callers must not modify it.
//
// Unversioned-only (RFC-0025): Lookup always builds FnKey with Version ""
// and so only ever sees the unversioned pool's entry -- a versioned pool's
// endpoints (Version != "") are invisible to it. This is deliberate for now:
// nothing routes to a specific version yet (see Admit's doc comment). Lookup
// will need a version parameter, mirroring Admit, before phase 3 introduces
// version-aware endpoint reads.
func (ix *Index) Lookup(namespace, name string) []Endpoint {
	key := FnKey{Namespace: namespace, Name: name}
	s := ix.shard(key)
	s.mu.RLock()
	e, ok := s.m[key]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	eps := e.eps.Load()
	if eps == nil {
		return nil
	}
	return *eps
}

// ReadyCount returns how many ready endpoints the function has.
//
// Unversioned-only (RFC-0025): built on Lookup, so it inherits the same
// Version "" restriction -- see Lookup's doc comment.
func (ix *Index) ReadyCount(namespace, name string) int {
	n := 0
	for _, ep := range ix.Lookup(namespace, name) {
		if ep.Ready {
			n++
		}
	}
	return n
}

// Size returns the number of functions currently present in the index.
func (ix *Index) Size() int {
	return int(ix.size.Load())
}

// fnKeyForSlice maps a slice to its function via the labels the EndpointSlice
// controller mirrors from the function's Service. ok=false for slices that do
// not carry function labels (not Fission-managed). Version comes from the
// same mirrored fission.io/function-version label, "" when the Service (and
// so the slice) carries none -- the unversioned pool.
func fnKeyForSlice(es *discoveryv1.EndpointSlice) (FnKey, bool) {
	name := es.Labels[fv1.FUNCTION_NAME]
	namespace := es.Labels[fv1.FUNCTION_NAMESPACE]
	if name == "" || namespace == "" {
		return FnKey{}, false
	}
	version := es.Labels[fv1.FUNCTION_VERSION]
	return FnKey{Namespace: namespace, Name: name, Version: version}, true
}

// endpointsFromSlice flattens one slice into Endpoints. The port is taken from
// the slice's first defined port (the function Services expose exactly one).
func endpointsFromSlice(es *discoveryv1.EndpointSlice) []Endpoint {
	var port int32
	for i := range es.Ports {
		if es.Ports[i].Port != nil {
			port = *es.Ports[i].Port
			break
		}
	}
	if port == 0 {
		return nil
	}
	eps := make([]Endpoint, 0, len(es.Endpoints))
	for i := range es.Endpoints {
		sep := &es.Endpoints[i]
		if len(sep.Addresses) == 0 {
			continue
		}
		ready := sep.Conditions.Ready == nil || *sep.Conditions.Ready
		var podUID types.UID
		if sep.TargetRef != nil && sep.TargetRef.Kind == "Pod" {
			podUID = sep.TargetRef.UID
		}
		address := fmt.Sprintf("%s:%d", sep.Addresses[0], port)
		// Parse once per slice event so the per-request hot path stays
		// allocation-free; address is IP:port, so this cannot fail in
		// practice — a parse error just drops the endpoint.
		u, err := url.Parse("http://" + address)
		if err != nil {
			continue
		}
		eps = append(eps, Endpoint{
			Address: address,
			URL:     u,
			PodUID:  podUID,
			Ready:   ready,
		})
	}
	return eps
}

// ApplySlice upserts one slice's endpoints and atomically swaps the function's
// merged list. ApplySlice and DeleteSlice assume callers are serialized per
// slice (today: the single informer event handler) — concurrent feeders could
// interleave the shard lookup and the entry rebuild and lose an update.
func (ix *Index) ApplySlice(es *discoveryv1.EndpointSlice) {
	key, ok := fnKeyForSlice(es)
	if !ok {
		return
	}
	sliceKey := es.Namespace + "/" + es.Name
	s := ix.shard(key)

	s.mu.Lock()
	e, exists := s.m[key]
	if !exists {
		e = &fnEntry{
			slices:   make(map[string][]Endpoint),
			counters: make(map[types.UID]*atomic.Int64),
		}
		s.m[key] = e
		ix.size.Add(1)
	}
	s.mu.Unlock()

	e.mu.Lock()
	e.slices[sliceKey] = endpointsFromSlice(es)
	// Any slice event for this function lifts quarantines (and pending
	// strikes): dead endpoints have been (or are being) removed by the slice
	// controller, so survivors are trustworthy again.
	e.quarantined.Store(nil)
	e.strikes = nil
	e.rebuildLocked()
	e.mu.Unlock()
}

// DeleteSlice removes one slice's endpoints; the function entry itself is
// dropped once its last slice is gone.
func (ix *Index) DeleteSlice(es *discoveryv1.EndpointSlice) {
	key, ok := fnKeyForSlice(es)
	if !ok {
		return
	}
	sliceKey := es.Namespace + "/" + es.Name
	s := ix.shard(key)

	s.mu.RLock()
	e, exists := s.m[key]
	s.mu.RUnlock()
	if !exists {
		return
	}

	e.mu.Lock()
	delete(e.slices, sliceKey)
	e.quarantined.Store(nil)
	e.strikes = nil
	empty := len(e.slices) == 0
	e.rebuildLocked()
	e.mu.Unlock()

	if empty {
		s.mu.Lock()
		// Re-check under the shard lock: a concurrent ApplySlice may have
		// repopulated the entry.
		if cur, ok := s.m[key]; ok && cur == e {
			cur.mu.Lock()
			if len(cur.slices) == 0 {
				delete(s.m, key)
				ix.size.Add(-1)
			}
			cur.mu.Unlock()
		}
		s.mu.Unlock()
	}
}

// rebuildLocked re-merges the per-slice endpoint lists into the copy-on-write
// snapshot, attaching each pod's shared in-flight counter (created on first
// sight, pruned when the pod leaves every slice). Caller holds e.mu.
func (e *fnEntry) rebuildLocked() {
	n := 0
	for _, eps := range e.slices {
		n += len(eps)
	}
	merged := make([]Endpoint, 0, n)
	live := make(map[types.UID]struct{}, n)
	for _, eps := range e.slices {
		for _, ep := range eps {
			if e.counters != nil && ep.PodUID != "" {
				c, ok := e.counters[ep.PodUID]
				if !ok {
					c = &atomic.Int64{}
					e.counters[ep.PodUID] = c
				}
				ep.inflight = c
				live[ep.PodUID] = struct{}{}
			}
			merged = append(merged, ep)
		}
	}
	for uid := range e.counters {
		if _, ok := live[uid]; !ok {
			delete(e.counters, uid)
		}
	}
	e.eps.Store(&merged)
}

// AdmitResult explains an Admit outcome, so the resolver can label its
// fallback metric and logs with the real refusal reason instead of a generic
// "saturated".
type AdmitResult string

const (
	Admitted        AdmitResult = "admitted"
	NoEntry         AdmitResult = "no_entry"
	AllBusy         AdmitResult = "all_busy"
	AllQuarantined  AdmitResult = "all_quarantined"
	NoCountedReady  AdmitResult = "no_counted_ready"
	AdmitContention AdmitResult = "cas_contention"
)

// hrwScore is the rendezvous (highest-random-weight) hash of (key, endpoint):
// a pure function, so every router replica ranks endpoints identically with
// no shared ring state (RFC-0023 S4), and removing an endpoint moves only the
// keys that ranked it first (S5).
func hrwScore(key, address string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(address))
	return h.Sum64()
}

// Admit picks a ready, non-quarantined endpoint with free capacity (below
// requestsPerPod), increments its in-flight counter, and returns it with a
// release func that the caller MUST invoke when the request completes
// (response done / stream drained). A non-Admitted result names why no
// endpoint was admissible.
//
// The pick: least-outstanding by default; with a non-empty stickyKey
// (RFC-0023 sticky routing) the highest HRW-ranked ADMISSIBLE endpoint
// instead — a branch in this scan, not a strategy object, so the fused
// pick+admit atomicity and the release-closure accounting seam are
// untouched. A saturated sticky winner is simply not admissible: the key
// overflows to its next-ranked endpoint rather than queueing (stickiness is
// an optimization, never a correctness dependency — durable truth lives
// behind the state API).
//
// Enforcement is per-router-replica by design (RFC-0002): worst-case
// over-admission is (replicas-1)×requestsPerPod per pod, degrading to brief
// queueing at the pod. Functions needing exact global accounting use the
// strict-mode annotation, which bypasses this path entirely.
//
// version selects which of a function's warm pools to admit from (RFC-0025):
// "" is the unversioned (live-spec) pool, today's only pool until phase 3
// starts stamping fission.io/function-version. A non-empty version admits
// only from that version's own FnKey entry -- it never falls back to, or
// bleeds into, another version's endpoints.
func (ix *Index) Admit(namespace, name, version string, requestsPerPod int, stickyKey string) (Endpoint, func(), AdmitResult) {
	if requestsPerPod <= 0 {
		requestsPerPod = 1
	}
	key := FnKey{Namespace: namespace, Name: name, Version: version}
	s := ix.shard(key)
	s.mu.RLock()
	e, ok := s.m[key]
	s.mu.RUnlock()
	if !ok {
		return Endpoint{}, nil, NoEntry
	}
	epsp := e.eps.Load()
	if epsp == nil {
		return Endpoint{}, nil, NoEntry
	}

	quarantined := e.quarantined.Load()
	var now time.Time
	if quarantined != nil {
		now = time.Now()
	}

	// Endpoint selection with a bounded CAS retry: the snapshot is
	// immutable, only the counters move.
	result := NoEntry
	for range 4 {
		var best *Endpoint
		var bestLoad int64
		var bestScore uint64
		counted, quarantinedN, busy := 0, 0, 0
		for i := range *epsp {
			ep := &(*epsp)[i]
			if !ep.Ready || ep.inflight == nil {
				continue
			}
			counted++
			if quarantined != nil {
				if expiry, bad := (*quarantined)[ep.Address]; bad && now.Before(expiry) {
					quarantinedN++
					continue
				}
			}
			load := ep.inflight.Load()
			if load >= int64(requestsPerPod) {
				busy++
				continue
			}
			if stickyKey != "" {
				if score := hrwScore(stickyKey, ep.Address); best == nil || score > bestScore {
					best, bestLoad, bestScore = ep, load, score
				}
				continue
			}
			if best == nil || load < bestLoad {
				best, bestLoad = ep, load
			}
		}
		if best == nil {
			switch {
			case counted == 0:
				return Endpoint{}, nil, NoCountedReady
			case quarantinedN == counted:
				return Endpoint{}, nil, AllQuarantined
			default:
				return Endpoint{}, nil, AllBusy
			}
		}
		if best.inflight.CompareAndSwap(bestLoad, bestLoad+1) {
			counter := best.inflight
			var once sync.Once
			release := func() { once.Do(func() { counter.Add(-1) }) }
			return *best, release, Admitted
		}
		// Lost the CAS to a concurrent admit — re-scan.
		result = AdmitContention
	}
	return Endpoint{}, nil, result
}

// Quarantine marks an address unadmissible until the next slice event for the
// function or the quarantine TTL expires, whichever comes first. The transport
// calls it on the FIRST dial failure of an index-admitted endpoint
// (re-resolving would just re-admit the same dead pod); only the legacy
// executor-resolved path climbs a retry ladder before invalidating. Each new
// (or expired-and-renewed) quarantine is counted in
// fission_router_endpointcache_quarantines_total.
//
// Unversioned-only (RFC-0025): builds FnKey with Version "", so it can only
// ever quarantine an address within the unversioned pool's entry -- see
// Lookup's doc comment. Quarantining a dial failure reported against a
// versioned endpoint would silently find no entry under "" and do nothing
// (the dead address stays admissible); this needs a version parameter
// before phase 3 introduces version-aware endpoints and callers that dial
// them.
func (ix *Index) Quarantine(namespace, name, address string) {
	key := FnKey{Namespace: namespace, Name: name}
	s := ix.shard(key)
	s.mu.RLock()
	e, ok := s.m[key]
	s.mu.RUnlock()
	if !ok {
		return
	}
	e.mu.Lock()
	stored := e.quarantineLocked(address, time.Now(), ix.quarantineTTL)
	e.mu.Unlock()
	if stored {
		RecordQuarantine()
	}
}

// quarantineLocked writes address into the copy-on-write quarantine map;
// callers hold e.mu. It reports whether a new (or expired-and-renewed)
// quarantine was stored — false means the address was already quarantined
// (idempotent: a dead-pod storm reports the same address from many in-flight
// requests, and only the first copy-and-store matters).
func (e *fnEntry) quarantineLocked(address string, now time.Time, ttl time.Duration) bool {
	// A quarantine consumes any pending strikes for the address: without this
	// a hard quarantine would leave stale strikes behind, and the first soft
	// failure after the TTL expires could escalate off old state.
	delete(e.strikes, address)
	cur := e.quarantined.Load()
	if cur != nil {
		if expiry, already := (*cur)[address]; already && now.Before(expiry) {
			return false
		}
	}
	next := make(map[string]time.Time)
	if cur != nil {
		// Copy only live entries: expired quarantines fall away here instead
		// of accumulating across the entry's lifetime.
		for a, expiry := range *cur {
			if now.Before(expiry) {
				next[a] = expiry
			}
		}
	}
	next[address] = now.Add(ttl)
	e.quarantined.Store(&next)
	return true
}

// ReportDialTimeout records a soft dial failure for an address, quarantining
// only after dialTimeoutStrikeLimit strikes land within one TTL window (see
// that constant for the saturation rationale). Strikes are cleared by slice
// events (like quarantines) and lapse with the window. The return value
// reports whether THIS call stored a new quarantine — false both below the
// limit and when a concurrent report already quarantined the address, so
// callers can log escalations without storm-duplicated noise.
//
// Unversioned-only (RFC-0025): builds FnKey with Version "" -- see
// Quarantine's doc comment. A dial timeout reported against a versioned
// endpoint would silently find no entry and record nothing (no strike, no
// eventual quarantine); needs a version parameter before phase 3.
func (ix *Index) ReportDialTimeout(namespace, name, address string) bool {
	key := FnKey{Namespace: namespace, Name: name}
	s := ix.shard(key)
	s.mu.RLock()
	e, ok := s.m[key]
	s.mu.RUnlock()
	if !ok {
		return false
	}
	now := time.Now()
	e.mu.Lock()
	// Timeouts reported while the address is already quarantined don't count:
	// in-flight requests keep failing after the first quarantine stores, and
	// their strikes would outlive the quarantine window and re-quarantine the
	// endpoint faster than dialTimeoutStrikeLimit once it's admissible again.
	if cur := e.quarantined.Load(); cur != nil {
		if expiry, already := (*cur)[address]; already && now.Before(expiry) {
			e.mu.Unlock()
			return false
		}
	}
	if e.strikes == nil {
		e.strikes = make(map[string]dialStrike)
	}
	rec := e.strikes[address]
	if now.After(rec.expiry) {
		rec.count = 0
	}
	rec.count++
	if rec.count == 1 {
		// The window is anchored at the FIRST strike (fixed, not sliding):
		// refreshing the expiry per strike would let sporadic timeouts —
		// one every ~TTL, never dialTimeoutStrikeLimit within any single
		// window — accumulate forever and quarantine a healthy pod.
		rec.expiry = now.Add(ix.quarantineTTL)
	}
	if rec.count >= dialTimeoutStrikeLimit {
		stored := e.quarantineLocked(address, now, ix.quarantineTTL)
		e.mu.Unlock()
		if stored {
			RecordQuarantine()
		}
		return stored
	}
	e.strikes[address] = rec
	e.mu.Unlock()
	RecordDialTimeoutStrike()
	return false
}
