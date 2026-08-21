// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// ErrEncode wraps a marshal failure from WriteJSON. It is the signal that
// NOTHING was written to the ResponseWriter, so the caller may still answer
// with a status of its own; any other error means the status and some bytes
// are already on the wire and only logging is left.
var ErrEncode = errors.New("encoding JSON response")

// WriteJSON writes v as an application/json response with the given status.
//
// v is marshaled BEFORE the status line is written, which is what makes the
// returned error actionable: on failure nothing has been sent, so the caller
// is still free to answer with an error status instead. Encoding straight into
// the ResponseWriter would instead commit a 200 and then emit a truncated
// body. That ordering is also what lets the handlers whose status depends on
// the marshal result (builder, fetcher) share this helper.
//
// Set any additional response headers before calling — they are frozen when
// the status is written.
func WriteJSON[T any](w http.ResponseWriter, status int, v T) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrEncode, err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, err = w.Write(b)
	return err
}
