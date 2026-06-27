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
func PooledTransport(maxIdleConnsPerHost int) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = maxIdleConnsPerHost
	if maxIdleConnsPerHost > t.MaxIdleConns {
		t.MaxIdleConns = maxIdleConnsPerHost
	}
	return t
}
