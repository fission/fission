// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"encoding/json"
	"net/http"
)

// WriteJSON writes v as an application/json response with the given status.
// Set any additional response headers before calling — the status line is
// written here, and headers are frozen with it.
//
// The returned error is the Encode failure after the header was already
// written; at that point it cannot change the response, so callers that
// don't log it may discard it.
func WriteJSON[T any](w http.ResponseWriter, status int, v T) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}
