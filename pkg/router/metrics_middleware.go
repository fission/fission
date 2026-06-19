// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"

	"github.com/gorilla/mux"

	"github.com/fission/fission/pkg/utils/httpmux"
	"github.com/fission/fission/pkg/utils/metrics"
)

// metricMiddleware records HTTP request metrics for the (still gorilla-backed)
// router, labelling each request by the matched route's path template. The
// gorilla CurrentRoute lookup is isolated here — the only place the router uses
// gorilla for metrics — and the recording (in-flight gauge, duration, status
// code, websocket bypass) is delegated to httpmux.Instrument so that logic
// lives in exactly one place. When the router migrates onto httpmux this is
// replaced by httpmux's WithMetrics option and deleted.
func metricMiddleware(next http.Handler) http.Handler {
	return httpmux.Instrument(metrics.HTTPRecorder{}, routePathTemplate, next)
}

// routePathTemplate returns the matched gorilla route's registered template
// (e.g. /fission-function/{name}), falling back to the raw request path when no
// route matched or the template is unavailable. gorilla's Use middleware runs
// after route matching, so CurrentRoute is populated by the time this runs.
func routePathTemplate(r *http.Request) string {
	if route := mux.CurrentRoute(r); route != nil {
		if tmpl, err := route.GetPathTemplate(); err == nil {
			return tmpl
		}
	}
	return r.URL.Path
}
