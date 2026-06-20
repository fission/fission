// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRenderInvocationFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		component string
		status    int
		body      string
		contains  []string
		absent    []string
	}{
		{
			name:      "structured executor failure names component, reason, status, and the body's request id",
			component: "executor",
			status:    503,
			body:      `{"component":"executor","reason":"specialization_failed","requestId":"req-abc"}`,
			contains:  []string{"executor", "specialization_failed", "503", "req-abc"},
		},
		{
			name:      "debug message is surfaced when present",
			component: "function",
			status:    500,
			body:      `{"component":"function","reason":"function_error","message":"panic: boom"}`,
			contains:  []string{"function", "function_error", "detail: panic: boom"},
		},
		{
			name:      "structured failure without a request id still renders",
			component: "timeout",
			status:    504,
			body:      `{"component":"timeout","reason":"function_timeout"}`,
			contains:  []string{"timeout", "function_timeout", "504"},
			absent:    []string{"request "},
		},
		{
			name:     "legacy plain-text body (no component header) falls back to the raw body",
			status:   502,
			body:     "upstream connect error",
			contains: []string{"returned 502", "upstream connect error"},
			absent:   []string{"failed in"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			renderInvocationFailure(&buf, "hello", tc.status, tc.component, []byte(tc.body))
			out := buf.String()
			for _, c := range tc.contains {
				assert.Containsf(t, out, c, "output:\n%s", out)
			}
			for _, a := range tc.absent {
				assert.NotContainsf(t, out, a, "output:\n%s", out)
			}
		})
	}
}
