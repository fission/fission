// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteJSON(t *testing.T) {
	t.Parallel()

	t.Run("writes the status, headers, and body", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()

		require.NoError(t, WriteJSON(rec, http.StatusAccepted, map[string]string{"id": "abc"}))

		assert.Equal(t, http.StatusAccepted, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		assert.JSONEq(t, `{"id":"abc"}`, rec.Body.String())
		assert.Equal(t, "12", rec.Header().Get("Content-Length"), "length of the marshaled body")
	})

	t.Run("headers set by the caller survive", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		rec.Header().Set("X-Fission-Invocation-Id", "inv-1")

		require.NoError(t, WriteJSON(rec, http.StatusOK, struct{}{}))

		assert.Equal(t, "inv-1", rec.Header().Get("X-Fission-Invocation-Id"))
	})

	// The reason this helper marshals before touching the ResponseWriter: a
	// caller that gets an error back must still be able to send its own status.
	t.Run("an unmarshalable value writes nothing at all", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()

		err := WriteJSON(rec, http.StatusOK, math.Inf(1))

		require.Error(t, err)
		assert.Empty(t, rec.Body.String(), "no partial body may be emitted")
		assert.Empty(t, rec.Header().Get("Content-Type"), "no headers committed")
		// The caller can still answer honestly.
		http.Error(rec, "internal error", http.StatusInternalServerError)
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}
