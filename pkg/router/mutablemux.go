// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"
	"os"
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
	// This should never happen, but if it does, log an error and exit.
	mr.logger.Error(nil, "router is nil")
	os.Exit(1)
}

func (mr *mutableRouter) updateRouter(newHandler *mux.Router) {
	mr.router.Store(newHandler)
}
