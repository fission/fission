// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"fmt"
	"path"

	apiv1 "k8s.io/api/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

const (
	// SharedSecretRoot and SharedConfigRoot are the in-pod roots a MountPath is
	// relative to. They match the fetcher's shared volume paths so a function
	// sees its files at the same location on every executor — the whole reason
	// RFC-0030 §4 constrains MountPath to a relative value rather than letting
	// it name any absolute path.
	SharedSecretRoot = "/secrets"
	SharedConfigRoot = "/configs"
)

// MountPathVolumes builds the native Volumes and VolumeMounts that give the
// CONTAINER executor the same file layout the fetcher produces elsewhere.
//
// The container executor runs no fetcher, so nothing writes these files for it.
// Kubernetes can project the objects directly instead, which is strictly better
// here (kubelet refreshes a projected volume in place), but it means the layout
// has to be reproduced rather than inherited.
//
// Mounts are constructed HERE from the validated reference, never passed
// through from user pod-spec input: that is what keeps a user-chosen path from
// landing on /var/run/secrets/kubernetes.io/serviceaccount or anywhere else in
// the image. Every path is rooted under SharedSecretRoot/SharedConfigRoot, and
// the reference's own MountPath is re-validated on the way in.
func MountPathVolumes(fn *fv1.Function) ([]apiv1.Volume, []apiv1.VolumeMount, error) {
	var volumes []apiv1.Volume
	var mounts []apiv1.VolumeMount

	add := func(kind, root, objName, mountPath, objNamespace string) error {
		sub, err := fv1.ObjectSubPath(mountPath, objNamespace, objName)
		if err != nil {
			return err
		}
		// Volume names must be DNS labels and unique within the pod; derive one
		// from the kind and object name rather than the path, which may contain
		// separators.
		volName := fmt.Sprintf("fission-%s-%s", kind, objName)
		vol := apiv1.Volume{Name: volName}
		if kind == "secret" {
			vol.VolumeSource = apiv1.VolumeSource{
				Secret: &apiv1.SecretVolumeSource{SecretName: objName},
			}
		} else {
			vol.VolumeSource = apiv1.VolumeSource{
				ConfigMap: &apiv1.ConfigMapVolumeSource{
					LocalObjectReference: apiv1.LocalObjectReference{Name: objName},
				},
			}
		}
		volumes = append(volumes, vol)
		mounts = append(mounts, apiv1.VolumeMount{
			Name: volName,
			// path.Join keeps the result under the root even if sub is oddly
			// spelled; ObjectSubPath has already refused anything that escapes.
			MountPath: path.Join(root, sub),
			ReadOnly:  true,
		})
		return nil
	}

	for _, s := range fn.Spec.Secrets {
		if s.MountPath == "" {
			// No redirect: the legacy envFrom projection still covers this
			// reference, and adding a volume for it would change behaviour for
			// functions that never opted in.
			continue
		}
		if err := add("secret", SharedSecretRoot, s.Name, s.MountPath, s.Namespace); err != nil {
			return nil, nil, err
		}
	}
	for _, c := range fn.Spec.ConfigMaps {
		if c.MountPath == "" {
			continue
		}
		if err := add("configmap", SharedConfigRoot, c.Name, c.MountPath, c.Namespace); err != nil {
			return nil, nil, err
		}
	}
	return volumes, mounts, nil
}
