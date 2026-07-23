// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"hash/fnv"
	"math/rand"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// functionWeightDistribution holds one function's slot in the cumulative
// weight distribution built by resolveByFunctionWeights; sumPrefix is the
// running total so selection is a binary search over prefixes.
type functionWeightDistribution struct {
	name      string
	weight    int
	sumPrefix int
}

// findCeil picks a function from the functionWeightDistribution list based on the
// random number generated. It uses the prefix calculated for the function weights.
func findCeil(randomNumber int, wtDistrList []functionWeightDistribution) string {
	low := 0
	high := len(wtDistrList) - 1

	for low < high {
		mid := (low + high) / 2
		if randomNumber >= wtDistrList[mid].sumPrefix {
			low = mid + 1
		} else {
			high = mid
		}
	}

	if wtDistrList[low].sumPrefix >= randomNumber {
		return wtDistrList[low].name
	}
	return ""
}

// stickyWeightHash is the FNV-64a hash of a sticky routing key, used to pick
// a weighted backend deterministically (RFC-0025 Task 5). Same style as
// endpointcache's rendezvous hrwScore (pkg/router/endpointcache/index.go): a
// pure function of the key alone, so every router replica -- and the pick
// site here vs. the Admit ranking downstream -- computes the identical
// value with no shared state.
func stickyWeightHash(key string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return h.Sum64()
}

// getCanaryBackend picks a function from fnWtDistributionList. With a sticky
// key, the pick is deterministic: FNV-64a hash of the key reduced mod the
// distribution's total weight, so the SAME key always lands on the same
// backend (until a weight change moves the boundary it straddles) -- request
// admission (transport.go, consuming the identical precomputed key) can never
// disagree with this pick. Without a key (unkeyed request, or the legacy
// FunctionReferenceTypeFunctionWeights canary, which has no sticky config to
// source a key from) the pick is uniform random, unchanged from pre-Task-5
// behavior modulo the off-by-one fix below.
//
// The random/hash draw is over [0, total) -- NOT [0, total] as the previous
// `rand.Intn(sumPrefix+1)` drew -- so a 100/0 split always picks the primary
// and a 0/100 split always picks the secondary; the old off-by-one gave the
// 100-weight entry a 1/(total+1) chance of losing anyway. findCeil's
// semantics (smallest entry whose sumPrefix > draw wins the boundary,
// draw==sumPrefix goes to the NEXT entry) already handle both draw spaces
// correctly, so only the draw's upper bound needed fixing.
func getCanaryBackend(fnMap map[string]*fv1.Function, fnWtDistributionList []functionWeightDistribution, stickyKey string) *fv1.Function {
	total := fnWtDistributionList[len(fnWtDistributionList)-1].sumPrefix
	if total <= 0 {
		// Degenerate (every weight zero): pre-existing edge case, not a Task-5
		// regression -- fall back to the first entry rather than panicking on
		// rand.Intn(0)/a modulo by zero.
		return fnMap[fnWtDistributionList[0].name]
	}

	var pick int
	if stickyKey != "" {
		pick = int(stickyWeightHash(stickyKey) % uint64(total))
	} else {
		pick = rand.Intn(total)
	}
	fnName := findCeil(pick, fnWtDistributionList)
	return fnMap[fnName]
}
