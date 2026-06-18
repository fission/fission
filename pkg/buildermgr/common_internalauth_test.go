// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// TestBuilderSigningNamespace pins buildermgr's version-aware signing decision —
// the sibling of the executor's. buildermgr must sign each builder pod's
// fetcher/builder sidecar call with the key that pod actually verifies with: a
// pod stamped with the namespace key-scheme annotation (created under dynamic
// tenancy) holds only its per-namespace keys, while pre-upgrade pods and all pods
// when tenancy is off verify master-derived. A wrong choice 401s the build.
func TestBuilderSigningNamespace(t *testing.T) {
	const builderNs = "team-a-builder"
	nsPod := &apiv1.Pod{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{fv1.AuthKeySchemeAnnotation: fv1.AuthKeySchemeNamespace},
	}}
	plainPod := &apiv1.Pod{}

	tests := []struct {
		name       string
		dynamic    bool
		pod        *apiv1.Pod
		wantNS     string
		wantScoped bool
	}{
		{"dynamic + ns-scheme pod signs with the builder namespace key", true, nsPod, builderNs, true},
		{"dynamic + pre-upgrade pod (no annotation) stays master-signed", true, plainPod, "", false},
		{"dynamic + nil pod stays master-signed", true, nil, "", false},
		{"non-dynamic ignores a stale annotation and stays master-signed", false, nsPod, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FISSION_DYNAMIC_NAMESPACES", strconv.FormatBool(tt.dynamic))
			ns, scoped := builderSigningNamespace(tt.pod, builderNs)
			assert.Equal(t, tt.wantScoped, scoped, "nsScoped decision")
			assert.Equal(t, tt.wantNS, ns, "signing namespace")
		})
	}
}
