// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// chat.js is a minimal multi-turn WebSocket "chat": it replies to each received
// message with "turn <n>: <message>", letting a client drive several round-trips
// over one long-lived socket. This exercises RFC-0008 streaming keepalive — the
// router must hold the function pod for the whole conversation, not just the
// first frame.
module.exports = async function (ws, clients) {
    let turn = 0;
    ws.on('message', function (data) {
        turn += 1;
        ws.send('turn ' + turn + ': ' + data);
    });
};
