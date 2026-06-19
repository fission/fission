// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"sync/atomic"

	"github.com/go-logr/logr"
)

//
// mutableRouter wraps the built mux handler and allows it to be atomically
// swapped as triggers / functions change. The handler is produced once per
// rebuild by httpmux.Mux.Handler() (routes + middleware compiled in) and stored
// whole, so a swap is a single pointer write and serving never sees a
// half-built mux.
//

type mutableRouter struct {
	logger  logr.Logger
	handler atomic.Pointer[http.Handler]
}

func newMutableRouter(logger logr.Logger, handler http.Handler) *mutableRouter {
	mr := mutableRouter{
		logger: logger.WithName("mutable_router"),
	}
	mr.updateRouter(handler)
	return &mr
}

func (mr *mutableRouter) ServeHTTP(responseWriter http.ResponseWriter, request *http.Request) {
	// Atomically grab the current handler and serve it.
	if handler := mr.handler.Load(); handler != nil && *handler != nil {
		(*handler).ServeHTTP(responseWriter, request)
		return
	}
	// This should never happen. Degrade gracefully with a 503 instead of
	// crashing the whole router process and dropping every other request.
	mr.logger.Error(nil, "router is nil")
	http.Error(responseWriter, "router not initialized", http.StatusServiceUnavailable)
}

func (mr *mutableRouter) updateRouter(handler http.Handler) {
	// Store the address of a fresh local so each swap publishes its own
	// *http.Handler; Load() in ServeHTTP reads whichever was last stored.
	// A nil handler argument stores a non-nil *http.Handler boxing a nil
	// interface — which is why ServeHTTP also checks *handler != nil (it
	// degrades to 503 rather than nil-dereferencing). Callers pass
	// Mux.Handler(), which never returns nil, so that guard is belt-and-braces.
	mr.handler.Store(&handler)
}
