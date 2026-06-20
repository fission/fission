// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package network

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
)

type (
	Error struct {
		err net.Error
	}
)

// Adapter returns an Error if the pass-in error is a network error;
// otherwise, nil will be returned.
func Adapter(err error) *Error {
	if err == nil {
		return nil
	}

	netErr, ok := err.(net.Error)
	if !ok {
		return nil
	}

	return &Error{err: netErr}
}

func (e Error) Error() string {
	return e.err.Error()
}

// IsDialError returns true if its a network dial error
func (e Error) IsDialError() bool {
	netOpErr, ok := e.err.(*net.OpError)
	if !ok {
		return false
	}

	if netOpErr.Op == "dial" {
		return true
	}

	return false
}

// IsConnRefusedError returns true if an error is a "connection refused" error.
// It matches both the raw *net.OpError carrying ECONNREFUSED (what a direct
// transport dial surfaces) via errors.Is, and the *url.Error string form (what
// an http.Client surfaces).
func (e Error) IsConnRefusedError() bool {
	if errors.Is(e.err, syscall.ECONNREFUSED) {
		return true
	}
	if urlErr, ok := errors.AsType[*url.Error](e.err); ok {
		return strings.Contains(urlErr.Error(), "connection refused")
	}
	return false
}

// IsTimeoutError returns true if its a network timeout error
func (e Error) IsTimeoutError() bool {
	if e.err.Timeout() {
		return true
	}

	opErr, ok := e.err.(*net.OpError)
	if ok {
		switch t := opErr.Err.(type) {
		case *os.SyscallError:
			if errno, ok := t.Err.(syscall.Errno); ok {
				switch errno {
				case syscall.ETIMEDOUT:
					return true
				}
			}
		}
	}

	return false
}

// IsUnsupportedProtoScheme returns true if an error is a "unsupported protocol scheme" error
func (e Error) IsUnsupportedProtoScheme() bool {
	urlErr, ok := e.err.(*url.Error)
	if !ok {
		return false
	}

	if strings.Contains(urlErr.Error(), "unsupported protocol scheme") {
		return true
	}

	return false
}
