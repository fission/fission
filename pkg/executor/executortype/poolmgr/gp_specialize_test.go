// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
)

// TestFetcherSigningNamespace pins the version-aware signing decision: the
// executor must sign each /specialize call with the key the target pod's fetcher
// actually verifies with, or the specialization 401s. A pod stamped with the
// namespace key-scheme annotation (created while dynamic tenancy was on for its
// namespace) holds only its per-namespace key; every other pod — pre-upgrade
// pods with no annotation, or any pod when tenancy is off — verifies with the
// master-derived key. Getting this wrong is a cross-tenant 401 storm (or, worse,
// a missed isolation boundary), so it is exercised directly.
func TestFetcherSigningNamespace(t *testing.T) {
	const podNS = "team-a"
	nsPod := &apiv1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace:   podNS,
		Annotations: map[string]string{fv1.AuthKeySchemeAnnotation: fv1.AuthKeySchemeNamespace},
	}}
	plainPod := &apiv1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: podNS}}

	tests := []struct {
		name       string
		dynamic    bool
		pod        *apiv1.Pod
		wantNS     string
		wantScoped bool
	}{
		{"dynamic on, ns-scheme pod signs with that namespace's key", true, nsPod, podNS, true},
		{"dynamic on, pre-upgrade pod (no annotation) stays master-signed", true, plainPod, "", false},
		{"dynamic off ignores a stale annotation and stays master-signed", false, nsPod, "", false},
		{"dynamic off, plain pod stays master-signed", false, plainPod, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FISSION_TENANCY_MODE", tenancyModeEnv(tt.dynamic))
			ns, scoped := fetcherSigningNamespace(tt.pod)
			assert.Equal(t, tt.wantScoped, scoped, "nsScoped decision")
			assert.Equal(t, tt.wantNS, ns, "signing namespace")
		})
	}
}

// TestShouldStampNamespaceKeyScheme pins the other half of the version-aware
// contract: the executor stamps the namespace key-scheme annotation onto a pool
// pod only when dynamic tenancy is on AND the pod's namespace is a live tenant —
// i.e. the tenant controller has already provisioned that namespace's derived-key
// Secret, so the pod will actually mount the per-namespace key it is being
// promised. A stamp without a matching key would 401 every specialization.
func TestShouldStampNamespaceKeyScheme(t *testing.T) {
	resolver := &utils.NamespaceResolver{}
	resolver.AddTenant("team-a")

	tests := []struct {
		name      string
		dynamic   bool
		namespace string
		resolver  *utils.NamespaceResolver
		want      bool
	}{
		{"dynamic on + tenant namespace stamps", true, "team-a", resolver, true},
		{"dynamic on + non-tenant namespace does not stamp", true, "other", resolver, false},
		{"dynamic off never stamps", false, "team-a", resolver, false},
		{"nil resolver never stamps", true, "team-a", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FISSION_TENANCY_MODE", tenancyModeEnv(tt.dynamic))
			assert.Equal(t, tt.want, shouldStampNamespaceKeyScheme(tt.namespace, tt.resolver))
		})
	}
}

// tenancyModeEnv maps the test's "dynamic?" flag to a FISSION_TENANCY_MODE value.
func tenancyModeEnv(dynamic bool) string {
	if dynamic {
		return "dynamic"
	}
	return "static"
}
