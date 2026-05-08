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
	"fmt"

	apiv1 "k8s.io/api/core/v1"
)

// FetcherSATokenVolumeName is the name of the projected volume that
// re-mounts the fission-fetcher ServiceAccount token onto the fetcher
// sidecar container only. The pod-level AutomountServiceAccountToken is
// set to false so the user-code container no longer inherits the
// namespace-wide secret-read powers of the fission-fetcher SA.
// See GHSA-85g2-pmrx-r49q.
const FetcherSATokenVolumeName = "fission-fetcher-sa-token"

// FetcherSATokenMountPath is the canonical Kubernetes ServiceAccount token
// mount path. The fetcher container mounts the projected volume here so
// the in-cluster client library finds its credentials in the expected
// place.
const FetcherSATokenMountPath = "/var/run/secrets/kubernetes.io/serviceaccount"

// FetcherContainerName is the name AddFetcherToPodSpec uses for the
// injected fetcher sidecar.
const FetcherContainerName = "fetcher"

// FetcherSATokenProjectedVolume returns the projected volume used to
// expose the fission-fetcher ServiceAccount token to the fetcher
// container only. It bundles the SA token, the cluster CA, and the
// pod's namespace so an in-cluster client built from this directory
// works the same as one built from the implicit auto-mount.
func FetcherSATokenProjectedVolume() apiv1.Volume {
	return apiv1.Volume{
		Name: FetcherSATokenVolumeName,
		VolumeSource: apiv1.VolumeSource{
			Projected: &apiv1.ProjectedVolumeSource{
				Sources: []apiv1.VolumeProjection{
					{
						ServiceAccountToken: &apiv1.ServiceAccountTokenProjection{
							Path:              "token",
							ExpirationSeconds: new(int64(3600)),
						},
					},
					{
						ConfigMap: &apiv1.ConfigMapProjection{
							LocalObjectReference: apiv1.LocalObjectReference{Name: "kube-root-ca.crt"},
							Items:                []apiv1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}},
						},
					},
					{
						DownwardAPI: &apiv1.DownwardAPIProjection{
							Items: []apiv1.DownwardAPIVolumeFile{
								{Path: "namespace", FieldRef: &apiv1.ObjectFieldSelector{FieldPath: "metadata.namespace"}},
							},
						},
					},
				},
			},
		},
	}
}

// MountFetcherSATokenOnFetcher locates the fetcher container in the
// given PodSpec and mounts the projected SA token at the canonical
// Kubernetes ServiceAccount path. It removes any pre-existing mount at
// that path first so the projected volume is the sole occupant —
// otherwise kubelet rejects the pod with a "duplicate mount path"
// error. Returns an error if no fetcher container is present, since a
// missing fetcher is almost certainly a bug in the calling code path
// rather than something we want to silently paper over.
func MountFetcherSATokenOnFetcher(podSpec *apiv1.PodSpec) error {
	for i := range podSpec.Containers {
		c := &podSpec.Containers[i]
		if c.Name != FetcherContainerName {
			continue
		}
		filtered := c.VolumeMounts[:0]
		for _, vm := range c.VolumeMounts {
			if vm.MountPath == FetcherSATokenMountPath {
				continue
			}
			filtered = append(filtered, vm)
		}
		c.VolumeMounts = append(filtered, apiv1.VolumeMount{
			Name:      FetcherSATokenVolumeName,
			MountPath: FetcherSATokenMountPath,
			ReadOnly:  true,
		})
		return nil
	}
	return fmt.Errorf("fetcher container not found in pod spec; cannot mount SA token volume")
}
