// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// logbody.js logs the request body it received, so tests can assert a unique
// per-test marker appears in the function logs (strings.Contains) instead of
// counting a fixed marker — count-based assertions are sensitive to pod churn
// re-baselining the visible logs.
module.exports = async function (context) {
    const body = context.request.body;
    const text = typeof body === "string" ? body : JSON.stringify(body);
    console.log("logbody:", text);
    return {
        status: 200,
        body: "logbody\n",
    };
};
