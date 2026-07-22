// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// RFC-0023 integration fixture: a get -> CAS-increment loop against the
// keyed-state API, SDK-less on purpose — plain HTTP over the two injected
// contract points (FISSION_STATE_URL env + the credentials file at
// FISSION_STATE_TOKEN_PATH written by the fetcher at specialize time).
const fs = require('fs');

function creds() {
    const raw = fs.readFileSync(process.env.FISSION_STATE_TOKEN_PATH, 'utf8');
    return JSON.parse(raw);
}

module.exports = async function (context) {
    const base = process.env.FISSION_STATE_URL;
    const c = creds();
    const hdrs = {
        'Authorization': 'Bearer ' + c.token,
        'X-Fission-State-Namespace': c.namespace,
        'X-Fission-State-Keyspace': c.keyspace,
    };

    for (let attempt = 0; attempt < 100; attempt++) {
        const g = await fetch(base + '/v1/state/counter', { headers: hdrs });
        let ver = 0, val = 0;
        if (g.status === 200) {
            ver = parseInt(g.headers.get('x-fission-state-version'), 10);
            val = parseInt(await g.text(), 10);
        } else if (g.status !== 404) {
            return { status: 500, body: 'get failed: ' + g.status + ' ' + (await g.text()) };
        }
        const r = await fetch(base + '/v1/state/counter/cas', {
            method: 'POST',
            headers: { ...hdrs, 'Content-Type': 'application/json' },
            body: JSON.stringify({
                expectVersion: ver,
                value: Buffer.from(String(val + 1)).toString('base64'),
            }),
        });
        if (r.status === 204) {
            return { status: 200, body: String(val + 1) };
        }
        if (r.status !== 412) { // 412 = lost the CAS race; retry
            return { status: 500, body: 'cas failed: ' + r.status + ' ' + (await r.text()) };
        }
    }
    return { status: 500, body: 'cas retries exhausted' };
};
