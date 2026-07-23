// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package timer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// TestFunctionTargetURL is the timer publisher's wiring test for RFC-0025:
// a TimeTrigger's embedded FunctionReference.Alias/Version threads through to
// the URL the cron actually fires at, via the shared
// utils.UrlForFunctionRef helper (table-tested on its own in
// pkg/utils/utils_test.go).
func TestFunctionTargetURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ref  fv1.FunctionReference
		ns   string
		sub  string
		want string
	}{
		{
			name: "bare reference: no suffix",
			ref:  fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn"},
			ns:   "default",
			sub:  "/",
			want: "/fission-function/fn/",
		},
		{
			name: "alias reference: :<alias> suffix",
			ref:  fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn", Alias: "blue"},
			ns:   "ns1",
			sub:  "/",
			want: "/fission-function/ns1/fn:blue/",
		},
		{
			name: "version reference: :<version> suffix",
			ref:  fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn", Version: "fn-v3"},
			ns:   "ns1",
			sub:  "/sub",
			want: "/fission-function/ns1/fn:fn-v3/sub",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tt := fv1.TimeTrigger{
				ObjectMeta: metav1.ObjectMeta{Name: "tt", Namespace: tc.ns},
				Spec: fv1.TimeTriggerSpec{
					FunctionReference: tc.ref,
					Subpath:           tc.sub,
				},
			}
			assert.Equal(t, tc.want, functionTargetURL(tt))
		})
	}
}
