// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package streaming holds pure, dependency-free helpers for the router's
// streaming proxy path: an idle Watchdog and an activity-tracking ReadCloser.
// It imports nothing from pkg/router, the Fission apis, or k8s so future
// consumers (e.g. RFC-0011's pkg/router/aigateway) can reuse it without an
// import cycle.
package streaming

import (
	"io"
	"sync"
)

// ActivityReadCloser wraps an io.ReadCloser, invoking onRead after each read
// that returns >0 bytes and onClose exactly once on the first Close. It is the
// hook the streaming path uses to re-arm the idle Watchdog and to fire the pod
// untap when an SSE/chunked stream finishes. Either callback may be nil.
type ActivityReadCloser struct {
	rc      io.ReadCloser
	onRead  func()
	onClose func()
	once    sync.Once
}

// NewActivityReadCloser wraps rc with read/close activity callbacks.
func NewActivityReadCloser(rc io.ReadCloser, onRead, onClose func()) *ActivityReadCloser {
	return &ActivityReadCloser{rc: rc, onRead: onRead, onClose: onClose}
}

func (a *ActivityReadCloser) Read(p []byte) (int, error) {
	n, err := a.rc.Read(p)
	if n > 0 && a.onRead != nil {
		a.onRead()
	}
	return n, err
}

func (a *ActivityReadCloser) Close() error {
	a.once.Do(func() {
		if a.onClose != nil {
			a.onClose()
		}
	})
	return a.rc.Close()
}
