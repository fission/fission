// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package backoff holds the platform's one exponential-backoff formula, so
// the async dispatcher and the workflow engine cannot drift on retry pacing.
package backoff

import "time"

// ExpFullJitter computes the delay before retry number attempt (1-based):
// base·2^(attempt-1), capped, with full jitter (uniform in [0, delay)) when
// rand is non-nil. rand is injected for deterministic tests and must return
// a float64 in [0, 1).
func ExpFullJitter(base, cap time.Duration, attempt int, rand func() float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := cap
	if shift := attempt - 1; shift < 62 {
		if e := base << shift; e > 0 && e < cap {
			delay = e
		}
	}
	if rand != nil {
		delay = time.Duration(rand() * float64(delay))
	}
	return delay
}
