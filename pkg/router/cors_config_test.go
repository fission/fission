// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// TestTriggerConfigError covers the CORS / ingress validation that moved from
// the (now-deleted) httptrigger admission webhook into the router: these checks
// need Go parsers (url.Parse, time.ParseDuration, regexp.CompilePOSIX) that CRD
// CEL cannot express, so buildMuxes skips invalid triggers and the router
// reports the result as a RouteAdmitted condition.
func TestTriggerConfigError(t *testing.T) {
	cases := []struct {
		name       string
		spec       fv1.HTTPTriggerSpec
		wantReason string // "" => valid (no error)
	}{
		{"empty config is valid", fv1.HTTPTriggerSpec{}, ""},
		{"valid cors origin", fv1.HTTPTriggerSpec{
			CorsConfig: &fv1.HTTPTriggerCorsConfig{AllowOrigins: []string{"https://app.example.com"}},
		}, ""},
		{"cors origin with path", fv1.HTTPTriggerSpec{
			CorsConfig: &fv1.HTTPTriggerCorsConfig{AllowOrigins: []string{"https://app.example.com/api"}},
		}, fv1.HTTPTriggerReasonInvalidCorsConfig},
		{"cors wildcard with credentials", fv1.HTTPTriggerSpec{
			CorsConfig: &fv1.HTTPTriggerCorsConfig{AllowOrigins: []string{"*"}, AllowCredentials: true},
		}, fv1.HTTPTriggerReasonInvalidCorsConfig},
		{"cors maxage not a duration", fv1.HTTPTriggerSpec{
			CorsConfig: &fv1.HTTPTriggerCorsConfig{MaxAge: "not-a-duration"},
		}, fv1.HTTPTriggerReasonInvalidCorsConfig},
		{"valid ingress path", fv1.HTTPTriggerSpec{
			IngressConfig: fv1.IngressConfig{Path: "/ok"},
		}, ""},
		{"ingress path not absolute", fv1.HTTPTriggerSpec{
			IngressConfig: fv1.IngressConfig{Path: "no-leading-slash"},
		}, fv1.HTTPTriggerReasonInvalidIngressConfig},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, err := triggerConfigError(&fv1.HTTPTrigger{Spec: tc.spec})
			if tc.wantReason == "" {
				assert.NoError(t, err)
				assert.Empty(t, reason)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tc.wantReason, reason)
		})
	}
}
