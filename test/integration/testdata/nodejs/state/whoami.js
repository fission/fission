// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// RFC-0023 sticky-routing fixture: echoes the serving pod so the test can
// assert per-key residency (all requests for one key land on one pod while
// the pod set is stable).
module.exports = async function (context) {
    return { status: 200, body: require('os').hostname() };
};
