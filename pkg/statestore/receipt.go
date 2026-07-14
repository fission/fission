// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// A lease receipt is an opaque, lease-scoped settle handle carried by
// LeasedMessage.Receipt: it embeds the durable message id AND the lease epoch, so
// a settle (Ack/Nack/Kill) can be rejected when it arrives for a stale lease
// (invariant Q2 / queue.tla I2). Every Queue driver mints and settles with the
// same encoding, so a receipt is portable across backends; it carries no
// integrity tag because drivers validate the epoch against live state on settle.
//
// The encoding is base64(id "\x00" epoch); decode splits on the last NUL, so ids
// may themselves contain NUL bytes (the epoch is decimal).

// EncodeReceipt builds a lease receipt from a durable id and lease epoch.
func EncodeReceipt(id string, epoch int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id + "\x00" + strconv.FormatInt(epoch, 10)))
}

// DecodeReceipt reverses EncodeReceipt. ok is false for any malformed input.
func DecodeReceipt(receipt string) (id string, epoch int64, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(receipt)
	if err != nil {
		return "", 0, false
	}
	sep := strings.LastIndexByte(string(raw), 0)
	if sep < 0 {
		return "", 0, false
	}
	epoch, err = strconv.ParseInt(string(raw[sep+1:]), 10, 64)
	if err != nil {
		return "", 0, false
	}
	return string(raw[:sep]), epoch, true
}
