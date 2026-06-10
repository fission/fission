// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package endpointcache holds the router's EndpointSlice-fed endpoint index
// (RFC-0002): a sharded, read-mostly map from function to its ready endpoints,
// maintained from slice informer events and read lock-free on the proxy hot
// path. In shadow mode it only powers a comparator against the executor's
// answers; at cutover it becomes the warm-path address source.
package endpointcache

import (
	"fmt"
	"hash/fnv"
	"sync"
	"sync/atomic"

	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

const shardCount = 64

type (
	// FnKey identifies a function by its CR coordinates (the labels mirrored
	// from the function's Service onto its slices).
	FnKey struct {
		Namespace string
		Name      string
	}

	// Endpoint is one slice endpoint, pre-resolved to a dialable address.
	Endpoint struct {
		// Address is host:port, the same form the executor returns for poolmgr
		// (podIP:8888).
		Address string
		// PodUID keys per-endpoint accounting across index rebuilds.
		PodUID types.UID
		// Ready mirrors the slice endpoint's ready condition.
		Ready bool
	}

	// Index is the sharded endpoint index. Writes (slice events) rebuild one
	// function's endpoint list copy-on-write; reads are a shard RLock plus an
	// atomic pointer load.
	Index struct {
		shards [shardCount]indexShard
		// size tracks the number of functions with at least one slice.
		size atomic.Int64
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
		// eps is the merged endpoint list, swapped copy-on-write. Hot-path
		// readers load it without taking mu.
		eps atomic.Pointer[[]Endpoint]
	}
)

// NewIndex returns an empty endpoint index.
func NewIndex() *Index {
	ix := &Index{}
	for i := range ix.shards {
		ix.shards[i].m = make(map[FnKey]*fnEntry)
	}
	return ix
}

func (ix *Index) shard(key FnKey) *indexShard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key.Namespace))
	_, _ = h.Write([]byte{'/'})
	_, _ = h.Write([]byte(key.Name))
	return &ix.shards[h.Sum32()%shardCount]
}

// Lookup returns the function's merged endpoint list (nil when unknown). The
// returned slice is immutable — callers must not modify it.
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
// not carry function labels (not Fission-managed).
func fnKeyForSlice(es *discoveryv1.EndpointSlice) (FnKey, bool) {
	name := es.Labels[fv1.FUNCTION_NAME]
	namespace := es.Labels[fv1.FUNCTION_NAMESPACE]
	if name == "" || namespace == "" {
		return FnKey{}, false
	}
	return FnKey{Namespace: namespace, Name: name}, true
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
		eps = append(eps, Endpoint{
			Address: fmt.Sprintf("%s:%d", sep.Addresses[0], port),
			PodUID:  podUID,
			Ready:   ready,
		})
	}
	return eps
}

// ApplySlice upserts one slice's endpoints and atomically swaps the function's
// merged list.
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
		e = &fnEntry{slices: make(map[string][]Endpoint)}
		s.m[key] = e
		ix.size.Add(1)
	}
	s.mu.Unlock()

	e.mu.Lock()
	e.slices[sliceKey] = endpointsFromSlice(es)
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
// snapshot. Caller holds e.mu.
func (e *fnEntry) rebuildLocked() {
	n := 0
	for _, eps := range e.slices {
		n += len(eps)
	}
	merged := make([]Endpoint, 0, n)
	for _, eps := range e.slices {
		merged = append(merged, eps...)
	}
	e.eps.Store(&merged)
}
