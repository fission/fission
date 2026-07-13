// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"encoding/base64"
	"strconv"
	"strings"
)

// A lease receipt is an opaque, lease-scoped settle handle: it embeds the
// durable message id AND the lease epoch, so a settle (Ack/Nack/Kill) can be
// rejected when it arrives for a stale lease (invariant Q2 / queue.tla I2). The
// encoding is base64(id "\x00" epoch); it carries no integrity tag because the
// in-memory driver validates the epoch against live state on settle, and the
// real drivers re-derive the guard from their epoch column.
func encodeReceipt(id string, epoch int64) string {
	raw := id + "\x00" + strconv.FormatInt(epoch, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeReceipt reverses encodeReceipt. ok is false for any malformed input.
func decodeReceipt(receipt string) (id string, epoch int64, ok bool) {
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
