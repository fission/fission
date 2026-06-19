// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpmux

import (
	"context"
	"net/http"
)

type ctxKey int

const varsKey ctxKey = iota

// withVars returns a shallow copy of r carrying the matched route's extracted
// path variables, so downstream handlers can read them via Vars. When the route
// captured no variables it returns r UNCHANGED — the common case (every static
// route, including all internal /fission-function/... invocations) then pays no
// per-request context allocation. Metrics do not flow through here: each route
// is instrumented with its registered pattern as a closure-captured constant
// (see instrument), so there is no need to carry the pattern per request.
func withVars(r *http.Request, vars map[string]string) *http.Request {
	if len(vars) == 0 {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), varsKey, vars))
}

// Vars returns the path variables extracted from the matched route template
// (e.g. {id} → "id"), or nil if the route had none. Replaces gorilla's
// mux.Vars.
func Vars(r *http.Request) map[string]string {
	if v, ok := r.Context().Value(varsKey).(map[string]string); ok {
		return v
	}
	return nil
}
