// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpx

import "net/http"

// PooledTransport returns a clone of http.DefaultTransport whose idle-connection
// pool is widened to maxIdleConnsPerHost, for internal clients that drive a
// SINGLE hot upstream (the executor, the router-internal listener) under
// concurrency. The stdlib default keeps only 2 idle connections per host, so
// beyond 2 concurrent requests each extra request dials a fresh TCP connection
// and discards it after use — connection churn that caps throughput and adds
// latency under load.
//
// Cloning preserves the default dial timeouts, proxy handling, and HTTP/2
// attempt; only the idle pool is changed (MaxIdleConns is raised to match when
// the per-host bound exceeds it, so the global cap can't clamp it). Each call
// returns an independent transport with its own pool, so give each client its
// own rather than mutating the process-wide http.DefaultTransport.
//
// maxIdleConnsPerHost should be > 0; a non-positive value leaves net/http's
// default of 2 (the churn this helper exists to avoid), so callers pass a real
// concurrency-sized bound.
func PooledTransport(maxIdleConnsPerHost int) *http.Transport {
	// Clone the stdlib *http.Transport (the normal case) to inherit its dial
	// timeouts/proxy/HTTP2 attempt; fall back to a zero transport if a dependency
	// has replaced http.DefaultTransport, so construction never panics at startup.
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		base = &http.Transport{}
	}
	t := base.Clone()
	t.MaxIdleConnsPerHost = maxIdleConnsPerHost
	if maxIdleConnsPerHost > t.MaxIdleConns {
		t.MaxIdleConns = maxIdleConnsPerHost
	}
	return t
}
