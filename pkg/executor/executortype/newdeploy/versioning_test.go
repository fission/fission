// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// fnForObjName builds a Function with a 36-char UUID-shaped UID (getObjName
// slices the last 17 chars of fn.UID, matching every real Kubernetes UID).
func fnForObjName(name, namespace string) *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       "83c82da2-81e9-4ebd-867e-f383e65e603f",
		},
	}
}

// TestGetObjName is a thin delegation smoke test: getObjName reaches
// executorUtils.VersionedObjName with the "newdeploy-" prefix and a
// versioned Function still yields a distinct, bounded name. The full
// length-bound property tests and hash-fallback table live once, against
// VersionedObjName itself, in pkg/executor/util/version_test.go.
func TestGetObjName(t *testing.T) {
	t.Parallel()
	deploy := &NewDeploy{}

	fn := fnForObjName("hello", "default")
	unversioned := deploy.getObjName(fn)
	assert.Equal(t, unversioned, deploy.getObjName(fn), "name must be stable")
	assert.Contains(t, unversioned, "newdeploy-")
	assert.LessOrEqual(t, len(unversioned), 63)

	fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v3"}
	versioned := deploy.getObjName(fn)
	assert.LessOrEqual(t, len(versioned), 63)
	assert.NotEqual(t, unversioned, versioned, "a versioned name must not collide with the unversioned one")
	assert.Contains(t, versioned, "-v3", "the -v<seq> tail must be recognizable in the derived name")
}

// TestGetDeployLabelsPropagatesFunctionVersion asserts FUNCTION_VERSION
// flows from the versioned Function object's labels into the Deployment
// labels via getDeployLabels' existing fnMeta.Labels merge — no new
// merge logic needed, just coverage that the merge actually carries the
// label through.
func TestGetDeployLabelsPropagatesFunctionVersion(t *testing.T) {
	t.Parallel()
	deploy := &NewDeploy{}
	fnMeta := metav1.ObjectMeta{
		Name:      "hello",
		Namespace: "default",
		UID:       "fn-uid",
		Labels:    map[string]string{fv1.FUNCTION_VERSION: "hello-v3"},
	}
	envMeta := metav1.ObjectMeta{Name: "env", Namespace: "default", UID: "env-uid"}

	labels := deploy.getDeployLabels(fnMeta, envMeta)

	assert.Equal(t, "hello-v3", labels[fv1.FUNCTION_VERSION], "FUNCTION_VERSION must propagate to Deployment labels")
}

// TestGetDeployLabelsUnversionedHasNoVersionLabel is the byte-identical
// control: an unversioned function's labels must not carry FUNCTION_VERSION.
func TestGetDeployLabelsUnversionedHasNoVersionLabel(t *testing.T) {
	t.Parallel()
	deploy := &NewDeploy{}
	fnMeta := metav1.ObjectMeta{Name: "hello", Namespace: "default", UID: "fn-uid"}
	envMeta := metav1.ObjectMeta{Name: "env", Namespace: "default", UID: "env-uid"}

	labels := deploy.getDeployLabels(fnMeta, envMeta)

	_, has := labels[fv1.FUNCTION_VERSION]
	assert.False(t, has, "unversioned function must not carry FUNCTION_VERSION label")
}
