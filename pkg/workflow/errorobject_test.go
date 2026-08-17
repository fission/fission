// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestErrorObjectWireFormat pins the encoded form of the two engine-owned
// error objects. They are user-visible (a Catch handler reads the error object
// as its input document) and persisted (RunState.Doc / RunState.Cause), and
// the shaped document feeds the StepScheduled InputHash — so the bytes, not
// just the fields, are the contract. Both were map[string]any literals before;
// the expected strings here are what those maps encoded to, key-sorted.
func TestErrorObjectWireFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		encode func() ([]byte, error)
		want   string
	}{
		{
			name: "catchError with an object cause",
			encode: func() ([]byte, error) {
				return json.Marshal(catchError{Cause: json.RawMessage(`{"reason":"past due"}`), ErrorType: "CardDeclined"})
			},
			want: `{"cause":{"reason":"past due"},"errorType":"CardDeclined"}`,
		},
		{
			name: "catchError with an absent cause",
			encode: func() ([]byte, error) {
				return json.Marshal(catchError{Cause: nonEmpty(nil), ErrorType: "Fission.FunctionError"})
			},
			want: `{"cause":null,"errorType":"Fission.FunctionError"}`,
		},
		{
			name: "branchErrorCause carries the branch through",
			encode: func() ([]byte, error) {
				return json.Marshal(branchErrorCause{Branch: "0", Cause: json.RawMessage(`"boom"`), ErrorType: "Fission.PermanentError"})
			},
			want: `{"branch":"0","cause":"boom","errorType":"Fission.PermanentError"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, err := tt.encode()
			require.NoError(t, err)
			// Byte equality, not JSONEq: a reordering would refingerprint the
			// documents of runs that are already in flight across an upgrade.
			assert.Equal(t, tt.want, string(raw))
		})
	}
}

// TestCatchErrorEmbedsRawCauseVerbatim pins that Cause is spliced in as JSON
// rather than re-encoded as a string — the failing step's cause is already
// JSON, and double-encoding it would hand catch handlers a quoted blob.
func TestCatchErrorEmbedsRawCauseVerbatim(t *testing.T) {
	t.Parallel()

	inner, err := json.Marshal(branchErrorCause{
		Branch:    "1",
		Cause:     json.RawMessage(`{"detail":"inner"}`),
		ErrorType: "Fission.PermanentError",
	})
	require.NoError(t, err)

	raw, err := json.Marshal(catchError{Cause: inner, ErrorType: "Fission.BranchFailed"})
	require.NoError(t, err)

	var decoded struct {
		Cause     branchErrorCause `json:"cause"`
		ErrorType string           `json:"errorType"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded), "cause must decode as a nested object, not a string")
	assert.Equal(t, "Fission.BranchFailed", decoded.ErrorType)
	assert.Equal(t, "1", decoded.Cause.Branch)
	assert.JSONEq(t, `{"detail":"inner"}`, string(decoded.Cause.Cause))
}
