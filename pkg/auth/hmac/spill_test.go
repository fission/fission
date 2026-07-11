// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package hmac

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpillReader(t *testing.T) {
	cases := []struct {
		name      string
		size      int
		threshold int64
		wantSpill bool
	}{
		{"empty stays in memory", 0, 1024, false},
		{"under threshold stays in memory", 500, 1024, false},
		{"at threshold stays in memory", 1024, 1024, false},
		{"over threshold spills to disk", 4096, 1024, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			payload := bytes.Repeat([]byte("x"), tc.size)
			want := sha256.Sum256(payload)

			sr, err := newSpillReader(bytes.NewReader(payload), tc.threshold, dir)
			require.NoError(t, err)

			// Hash matches the slice SHA-256 of the full body.
			assert.Equal(t, hex.EncodeToString(want[:]), sr.BodyHashHex())

			// A temp file exists iff we expected to spill.
			entries, _ := os.ReadDir(dir)
			if tc.wantSpill {
				assert.NotEmpty(t, entries, "expected a spill temp file")
			} else {
				assert.Empty(t, entries, "did not expect a temp file for an in-memory body")
			}

			// Body replays byte-identically.
			got, err := io.ReadAll(sr)
			require.NoError(t, err)
			assert.Equal(t, payload, got)

			// Close removes any temp file.
			require.NoError(t, sr.Close())
			entries, _ = os.ReadDir(dir)
			assert.Empty(t, entries, "Close must remove the spill temp file")
		})
	}
}

// TestSpillReaderCrossesThresholdAcrossChunks feeds the body one byte per read
// so the threshold is crossed mid-stream (prefix in memory, remainder to disk),
// proving the prefix is flushed and the hash spans both.
func TestSpillReaderCrossesThresholdAcrossChunks(t *testing.T) {
	payload := bytes.Repeat([]byte("ab"), 1000) // 2000 bytes
	want := sha256.Sum256(payload)

	sr, err := newSpillReader(iotest.OneByteReader(bytes.NewReader(payload)), 512, t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, hex.EncodeToString(want[:]), sr.BodyHashHex())
	got, err := io.ReadAll(sr)
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	require.NoError(t, sr.Close())
}
