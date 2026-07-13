// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// FuzzReceiptRoundTrip: encoding then decoding a lease receipt recovers the exact
// (id, epoch), for any id — including ids containing the NUL separator byte,
// since the epoch is decimal and the decoder splits on the last NUL.
func FuzzReceiptRoundTrip(f *testing.F) {
	f.Add("asyncinv/ns/1", int64(0))
	f.Add("", int64(9223372036854775807))
	f.Add("id\x00with\x00nuls", int64(42))
	f.Add("q/2", int64(-1))
	f.Fuzz(func(t *testing.T, id string, epoch int64) {
		gotID, gotEpoch, ok := decodeReceipt(encodeReceipt(id, epoch))
		require.True(t, ok)
		require.Equal(t, id, gotID)
		require.Equal(t, epoch, gotEpoch)
	})
}

// FuzzDecodeReceipt: decoding arbitrary input never panics, and any input it
// accepts round-trips through re-encoding (idempotence).
func FuzzDecodeReceipt(f *testing.F) {
	f.Add("")
	f.Add("not-base64!!")
	f.Add("YWJj") // "abc", no NUL separator
	f.Fuzz(func(t *testing.T, s string) {
		id, epoch, ok := decodeReceipt(s)
		if !ok {
			return
		}
		id2, epoch2, ok2 := decodeReceipt(encodeReceipt(id, epoch))
		require.True(t, ok2)
		require.Equal(t, id, id2)
		require.Equal(t, epoch, epoch2)
	})
}
