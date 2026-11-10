// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package endpointcache

// PredictSticky is the PURE rendezvous pick over ready endpoints — the same
// hrwScore ranking the router's sticky admission uses in Admit (index.go),
// with ZERO admission side effects: it reads only the eps slice it is
// handed, never touches the Index, an fnEntry, inflight counters,
// quarantine state, or the sticky-bucket teleport sketch (noteStickyPick).
//
// Callers (the agent runtime's best-effort CurrentPod prediction) MUST treat
// the result as UI truth, never routing truth: it is a guess at which pod
// Admit would pick for stickyKey right now, not a reservation, and it can be
// stale or simply wrong the instant a slice event, a quarantine, or load
// changes the real pick. Never call PredictSticky expecting it to affect, or
// substitute for, Admit's admission accounting — this file exists precisely
// so a prediction path can never mix into that accounting (CLAUDE.md).
//
// Ranking: the ready endpoint with the highest hrwScore(stickyKey,
// ep.Address); ties broken deterministically on the lexicographically
// smaller Address so the pick never depends on eps' slice order. Returns
// (Endpoint{}, false) when eps is empty or none of its endpoints are Ready.
func PredictSticky(eps []Endpoint, stickyKey string) (Endpoint, bool) {
	return pickHighestScore(eps, func(ep Endpoint) uint64 {
		return hrwScore(stickyKey, ep.Address)
	})
}

// pickHighestScore selects the Ready endpoint with the highest score,
// breaking ties on the lexicographically smaller Address. Factored out of
// PredictSticky so the tie-break is directly testable: a real hrwScore
// collision between two distinct addresses is astronomically unlikely to
// construct by hand (64-bit hash), so tests exercise this helper with a
// stub score function that manufactures ties on demand.
func pickHighestScore(eps []Endpoint, score func(Endpoint) uint64) (Endpoint, bool) {
	var best Endpoint
	var bestScore uint64
	found := false
	for _, ep := range eps {
		if !ep.Ready {
			continue
		}
		s := score(ep)
		if !found || s > bestScore || (s == bestScore && ep.Address < best.Address) {
			best, bestScore, found = ep, s, true
		}
	}
	return best, found
}
