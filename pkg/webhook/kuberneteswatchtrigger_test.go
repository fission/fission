// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
)

func makeValidKWT(triggerNs, specNs string) *v1.KubernetesWatchTrigger {
	return &v1.KubernetesWatchTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kwt-1",
			Namespace: triggerNs,
		},
		Spec: v1.KubernetesWatchTriggerSpec{
			Namespace: specNs,
			Type:      "POD",
			FunctionReference: v1.FunctionReference{
				Type: v1.FunctionReferenceTypeFunctionName,
				Name: "fn-1",
			},
		},
	}
}

func TestKubernetesWatchTriggerWebhook_Validate_CrossNamespace(t *testing.T) {
	cases := []struct {
		name         string
		triggerNs    string
		specNs       string
		wantRejected bool
		wantCrossErr bool // expect the new cross-namespace error specifically
	}{
		// Namespace presence/format is now enforced by the API server: the CRD
		// marks spec.namespace required with a DNS-1123 pattern, so the webhook
		// no longer re-checks it. The webhook keeps only the cross-namespace rule
		// (which needs the object's own namespace — not available to CRD CEL).
		{name: "same namespace is accepted", triggerNs: "default", specNs: "default", wantRejected: false},
		{name: "cross-namespace target is rejected by new check", triggerNs: "ns-attacker", specNs: "ns-victim", wantRejected: true, wantCrossErr: true},
	}

	r := &KubernetesWatchTrigger{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := r.Validate(makeValidKWT(tc.triggerNs, tc.specNs))
			if !tc.wantRejected {
				if err != nil {
					t.Fatalf("expected acceptance, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected rejection, got nil")
			}
			if tc.wantCrossErr {
				if !strings.Contains(err.Error(), "spec.namespace must equal the trigger namespace") {
					t.Fatalf("expected cross-namespace error, got: %v", err)
				}
				if !strings.Contains(err.Error(), tc.triggerNs) || !strings.Contains(err.Error(), tc.specNs) {
					t.Fatalf("error should mention both namespaces (%q and %q), got: %v", tc.triggerNs, tc.specNs, err)
				}
			}
		})
	}
}
