// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Warm-path steady load (RFC-0002 verification runbook): constant VUs against
// one pre-warmed poolmgr function. With the EndpointSlice gates on, every one
// of these requests should be admitted from the router's slice-fed index with
// zero executor RPCs; with the gates off each pays the executor lookup RPC.
// The off-vs-on delta in p99 (and the hit/fallback counters) is the
// acceptance signal.
import http from "k6/http";
import { check } from "k6";

export let options = {
    scenarios: {
        warm: {
            executor: "constant-vus",
            vus: Number(__ENV.VUS || 50),
            duration: __ENV.DURATION || "60s",
        },
    },
};

export default function () {
    let res = http.get(`${__ENV.FN_ENDPOINT}`, { timeout: "30s" });
    check(res, {
        "status is 200": (r) => r.status === 200,
    });
}
