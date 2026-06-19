// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httpmux

import (
	"context"
	"net/http"
)

type ctxKey int

const (
	varsKey ctxKey = iota
	patternKey
)

// withMatch returns a shallow copy of r carrying the matched route's pattern
// and (optionally) its extracted path variables, so downstream handlers can
// read them via Pattern / Vars.
func withMatch(r *http.Request, pattern string, vars map[string]string) *http.Request {
	ctx := context.WithValue(r.Context(), patternKey, pattern)
	if len(vars) > 0 {
		ctx = context.WithValue(ctx, varsKey, vars)
	}
	return r.WithContext(ctx)
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

// Pattern returns the registered pattern of the route that matched the request
// (e.g. "/fission-function/{name}"), or "" if no route matched. It is the
// low-cardinality label for metrics/logging — replaces gorilla's
// mux.CurrentRoute(r).GetPathTemplate().
func Pattern(r *http.Request) string {
	if p, ok := r.Context().Value(patternKey).(string); ok {
		return p
	}
	return ""
}
