// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// RFC-0023 integration fixture for S1 (scope isolation): presents this
// function's OWN token with a caller-chosen target keyspace claim and echoes
// statesvc's verdict. Anything but 403 for a foreign keyspace is an
// isolation break.
const fs = require('fs');

module.exports = async function (context) {
    const c = JSON.parse(fs.readFileSync(process.env.FISSION_STATE_TOKEN_PATH, 'utf8'));
    const target = (context.request.query && context.request.query.target) || c.keyspace;
    const r = await fetch(process.env.FISSION_STATE_URL + '/v1/state/probe', {
        headers: {
            'Authorization': 'Bearer ' + c.token,
            'X-Fission-State-Namespace': c.namespace,
            'X-Fission-State-Keyspace': target,
        },
    });
    return { status: 200, body: String(r.status) };
};
