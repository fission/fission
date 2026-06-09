// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// delayed.js delays ~4s before responding. It exercises RFC-0008 streaming:
// with `--streaming` the router must NOT cut this slow response at the
// function timeout (the idle timeout governs instead), whereas a classic
// function with the same low --fntimeout is cut.
module.exports = async function (context) {
    await new Promise((resolve) => setTimeout(resolve, 4000));
    return {
        status: 200,
        body: "streamed-after-delay\n",
    };
};
