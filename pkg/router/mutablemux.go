// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"sync/atomic"

	"github.com/go-logr/logr"
	"github.com/gorilla/mux"
)

//
// mutableRouter wraps the mux router, and allows the router to be
// atomically changed.
//

type mutableRouter struct {
	logger logr.Logger
	router atomic.Pointer[mux.Router]
}

func newMutableRouter(logger logr.Logger, handler *mux.Router) *mutableRouter {
	mr := mutableRouter{
		logger: logger.WithName("mutable_router"),
	}
	mr.router.Store(handler)
	return &mr
}

func (mr *mutableRouter) ServeHTTP(responseWriter http.ResponseWriter, request *http.Request) {
	// Atomically grab the underlying mux router and call it.
	if router := mr.router.Load(); router != nil {
		router.ServeHTTP(responseWriter, request)
		return
	}
	// This should never happen. Degrade gracefully with a 503 instead of
	// crashing the whole router process and dropping every other request.
	mr.logger.Error(nil, "router is nil")
	http.Error(responseWriter, "router not initialized", http.StatusServiceUnavailable)
}

func (mr *mutableRouter) updateRouter(newHandler *mux.Router) {
	mr.router.Store(newHandler)
}
