// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
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

// getCanaryBackend picks a function to route to based on a random number
// generated over [0, total) -- NOT [0, total] as the previous
// `rand.Intn(sumPrefix+1)` drew. The old inclusive draw had total+1 possible
// outcomes spread over weights summing to only total, so the boundary
// outcome (draw == total) always fell to the last entry: a 50/50 split
// actually routed 51/101 vs 50/101, and a 100/0 split still gave the
// zero-weight entry a 1/101 chance. findCeil's semantics (smallest entry
// whose sumPrefix > draw wins the boundary) already partition a half-open
// draw space correctly -- every entry gets exactly its own weight's share of
// outcomes -- so only the draw's upper bound needed fixing.
func getCanaryBackend(fnMap map[string]*fv1.Function, fnWtDistributionList []functionWeightDistribution) *fv1.Function {
	total := fnWtDistributionList[len(fnWtDistributionList)-1].sumPrefix
	if total <= 0 {
		// Degenerate (every weight zero): weights are validated elsewhere to
		// disallow this, but fall back to the first entry rather than
		// panicking on rand.Intn(0).
		return fnMap[fnWtDistributionList[0].name]
	}
	randomNumber := rand.Intn(total)
	fnName := findCeil(randomNumber, fnWtDistributionList)
	return fnMap[fnName]
}
