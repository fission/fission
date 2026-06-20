// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package error

import "fmt"

// Component names the part of the request path responsible for a failed
// function invocation (RFC-0015). It is surfaced to the caller in the
// structured error body and the X-Fission-Component response header so a
// failure can be attributed without reading server logs.
type Component string

const (
	ComponentRouter   Component = "router"
	ComponentExecutor Component = "executor"
	ComponentFetcher  Component = "fetcher"
	ComponentFunction Component = "function"
	ComponentTimeout  Component = "timeout"
)

// Stable invocation-failure reasons. These are part of the caller-facing
// contract (they appear in the error body), so values must not change once
// shipped; add new ones rather than renaming.
const (
	ReasonFunctionTimeout      = "function_timeout"
	ReasonStreamIdle           = "stream_idle"
	ReasonStreamMaxDuration    = "stream_max_duration"
	ReasonClientDisconnect     = "client_disconnect"
	ReasonSpecializationFailed = "specialization_failed"
	ReasonCapacityExceeded     = "capacity_exceeded"
	ReasonExecutorUnavailable  = "executor_unavailable"
	ReasonConnectionRefused    = "connection_refused"
	ReasonDialError            = "dial_error"
	ReasonFunctionError        = "function_error"
)

// InvocationError attributes a failed function invocation to a Component and a
// stable Reason. It is used two ways: the router round-tripper returns it as a
// sentinel wrapping the underlying error (so the proxy error handler can read
// the attribution via errors.As), and — once the handler fills in RequestID /
// TraceID — it is the JSON body returned to the caller. The wrapped error is
// preserved via Unwrap so GetHTTPError still derives the correct status code
// and errors.Is keeps working (e.g. context.Canceled detection).
type InvocationError struct {
	Component Component `json:"component"`
	Reason    string    `json:"reason"`
	RequestID string    `json:"requestId,omitempty"`
	TraceID   string    `json:"traceId,omitempty"`
	// Message carries verbose detail and is populated only when the caller
	// opted in (X-Fission-Debug) and the router runs in debug mode; otherwise
	// it is omitted so internal detail never leaks by default.
	Message string `json:"message,omitempty"`

	// err is the underlying cause. Unexported so it is never marshaled into
	// the response body; surfaced through Unwrap and Error.
	err error
}

// NewInvocationError builds an attribution sentinel wrapping cause.
func NewInvocationError(component Component, reason string, cause error) *InvocationError {
	return &InvocationError{Component: component, Reason: reason, err: cause}
}

func (e *InvocationError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%s/%s: %v", e.Component, e.Reason, e.err)
	}
	return fmt.Sprintf("%s/%s", e.Component, e.Reason)
}

// Unwrap exposes the underlying cause so errors.Is/As and GetHTTPError keep
// working across the attribution wrapper.
func (e *InvocationError) Unwrap() error { return e.err }
