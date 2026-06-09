// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package streaming

import (
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

func TestActivityReadCloser(t *testing.T) {
	t.Parallel()
	var reads, closes atomic.Int64
	arc := NewActivityReadCloser(
		io.NopCloser(strings.NewReader("hello")),
		func() { reads.Add(1) },
		func() { closes.Add(1) },
	)
	buf := make([]byte, 2)
	for {
		_, err := arc.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if reads.Load() == 0 {
		t.Fatal("onRead never fired")
	}
	if err := arc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := arc.Close(); err != nil { // idempotent
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatalf("onClose fired %d times, want 1", closes.Load())
	}
}

// TestActivityReadCloserNilCallbacks ensures nil callbacks are tolerated.
func TestActivityReadCloserNilCallbacks(t *testing.T) {
	t.Parallel()
	arc := NewActivityReadCloser(io.NopCloser(strings.NewReader("x")), nil, nil)
	if _, err := io.ReadAll(arc); err != nil {
		t.Fatal(err)
	}
	if err := arc.Close(); err != nil {
		t.Fatal(err)
	}
}
