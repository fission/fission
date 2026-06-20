// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package network

import (
	"errors"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsConnRefusedError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			// The form the reverse-proxy transport actually surfaces — a raw
			// *net.OpError carrying ECONNREFUSED. The old implementation bailed
			// before reaching its own syscall branch and returned false here.
			name: "net.OpError ECONNREFUSED",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}},
			want: true,
		},
		{
			name: "url.Error connection refused",
			err:  &url.Error{Op: "Get", URL: "http://x", Err: errors.New("connection refused")},
			want: true,
		},
		{
			name: "dial timeout is not connection refused",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("i/o timeout")},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			netErr := Adapter(tc.err)
			require.NotNil(t, netErr)
			assert.Equal(t, tc.want, netErr.IsConnRefusedError())
		})
	}
}
