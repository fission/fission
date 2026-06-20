// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package error

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInvocationError_Unwrap(t *testing.T) {
	t.Parallel()
	inner := MakeError(ErrorTooManyRequests, "busy")
	ie := NewInvocationError(ComponentExecutor, ReasonCapacityExceeded, inner)

	require.ErrorIs(t, ie, inner, "InvocationError must unwrap to its cause")
	assert.Equal(t, ComponentExecutor, ie.Component)
	assert.Equal(t, ReasonCapacityExceeded, ie.Reason)
	assert.Contains(t, ie.Error(), "busy")
}

// The status code a caller sees must not change when the router wraps an
// error for attribution: GetHTTPError unwraps to the inner ferror.Error.
func TestInvocationError_PreservesHTTPStatusOnUnwrap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		inner error
		want  int
	}{
		{name: "too many requests", inner: MakeError(ErrorTooManyRequests, "busy"), want: http.StatusTooManyRequests},
		{name: "not found", inner: MakeError(ErrorNotFound, "gone"), want: http.StatusNotFound},
		{name: "plain error defaults 500", inner: errors.New("boom"), want: http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ie := NewInvocationError(ComponentExecutor, ReasonSpecializationFailed, tc.inner)
			status, _ := GetHTTPError(ie)
			assert.Equal(t, tc.want, status)
		})
	}
}

func TestInvocationError_AsExtractable(t *testing.T) {
	t.Parallel()
	wrapped := error(NewInvocationError(ComponentFunction, ReasonConnectionRefused, errors.New("refused")))

	var ie *InvocationError
	require.True(t, errors.As(wrapped, &ie))
	assert.Equal(t, ComponentFunction, ie.Component)
	assert.Equal(t, ReasonConnectionRefused, ie.Reason)
}
