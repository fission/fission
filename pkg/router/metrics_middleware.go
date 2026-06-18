// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net/http"

	"github.com/gorilla/mux"

	"github.com/fission/fission/pkg/utils/metrics"
)

// metricMiddleware is the router's gorilla-aware HTTP metrics middleware. It
// records request metrics keyed on the matched route's path template (low
// cardinality) rather than the raw URL. The gorilla CurrentRoute lookup lives
// here — in the only package that still depends on gorilla/mux — so the shared
// metrics package stays router-agnostic.
func metricMiddleware(next http.Handler) http.Handler {
	return metrics.InstrumentHandlerFunc(routePathTemplate, next)
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
