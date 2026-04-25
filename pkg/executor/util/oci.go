/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package util

import (
	"context"

	apiv1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

// GetFunctionOCIArchive returns the OCIArchive referenced by the Function's
// deployment Package, or nil if the Function has no PackageRef or the Package
// uses a tarball/literal/url archive. A missing Package is not an error here:
// the legacy tarball flow is also robust to that timing during creation.
func GetFunctionOCIArchive(ctx context.Context, fissionClient versioned.Interface, fn *fv1.Function) (*fv1.OCIArchive, error) {
	ref := fn.Spec.Package.PackageRef
	if ref.Name == "" {
		return nil, nil
	}
	pkg, err := fissionClient.CoreV1().Packages(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return pkg.Spec.Deployment.OCI, nil
}

// MergeImagePullSecrets combines a single env-level pull secret name and the
// per-OCI list, deduplicating by Name. Empty inputs are skipped. The returned
// slice is nil when both inputs are empty so existing pod-spec defaults remain
// untouched.
func MergeImagePullSecrets(envSecret string, ociSecrets []apiv1.LocalObjectReference) []apiv1.LocalObjectReference {
	if envSecret == "" && len(ociSecrets) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, 1+len(ociSecrets))
	out := make([]apiv1.LocalObjectReference, 0, 1+len(ociSecrets))
	add := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		out = append(out, apiv1.LocalObjectReference{Name: name})
	}
	add(envSecret)
	for _, s := range ociSecrets {
		add(s.Name)
	}
	return out
}

// ApplyOCIImagePullSecrets sets pod.Spec.ImagePullSecrets to the merged list
// of envSecret and ociSecrets. Unlike ApplyImagePullSecret, this overwrites
// rather than no-ops when the merged list is empty, which matches OCI semantics
// where the OCIArchive is the source of truth.
func ApplyOCIImagePullSecrets(envSecret string, ociSecrets []apiv1.LocalObjectReference, podSpec apiv1.PodSpec) *apiv1.PodSpec {
	merged := MergeImagePullSecrets(envSecret, ociSecrets)
	if merged != nil {
		podSpec.ImagePullSecrets = merged
	}
	return &podSpec
}
