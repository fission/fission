// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/client-go/discovery"
)

// OCIImageVolumeName is the pod volume name used for OCI package image
// volumes (RFC-0001 Path B). Distinct from the fetcher's "userfunc" emptyDir
// so the two can coexist on newdeploy pods.
const OCIImageVolumeName = "oci-package-image"

// AddImageVolume appends an OCI image volume holding the package filesystem
// and mounts it read-only at mountPath on each named container. Pull secrets
// append to pod.Spec.ImagePullSecrets — the kubelet resolves image-volume
// pulls from those (plus the service account's). Call this AFTER every
// MergePodSpec so a runtime pod spec cannot strip or shadow the mount.
func AddImageVolume(podSpec *apiv1.PodSpec, image, subPath, mountPath string, pullSecrets []apiv1.LocalObjectReference, containerNames ...string) {
	podSpec.Volumes = append(podSpec.Volumes, apiv1.Volume{
		Name: OCIImageVolumeName,
		VolumeSource: apiv1.VolumeSource{
			Image: &apiv1.ImageVolumeSource{
				Reference:  image,
				PullPolicy: apiv1.PullIfNotPresent,
			},
		},
	})
	mount := apiv1.VolumeMount{
		Name:      OCIImageVolumeName,
		MountPath: mountPath,
		SubPath:   subPath,
		ReadOnly:  true,
	}
	for i := range podSpec.Containers {
		for _, name := range containerNames {
			if podSpec.Containers[i].Name == name {
				podSpec.Containers[i].VolumeMounts = append(podSpec.Containers[i].VolumeMounts, mount)
			}
		}
	}
	podSpec.ImagePullSecrets = append(podSpec.ImagePullSecrets, pullSecrets...)
}

// OCIImageVolumeEnabled reports whether the operator opted into delivering
// OCI packages via kubelet image volumes (RFC-0001 Path B) by setting
// ENABLE_OCI_IMAGE_VOLUME=true on the executor. Default off: Path B needs
// Kubernetes >= 1.33 (KEP-4639), above the supported floor.
func OCIImageVolumeEnabled() bool {
	enabled, err := strconv.ParseBool(os.Getenv("ENABLE_OCI_IMAGE_VOLUME"))
	return err == nil && enabled
}

// ImageVolumeSupported reports whether the cluster can mount OCI images as
// pod volumes: KEP-4639 image volumes are beta and on by default from
// Kubernetes 1.33. Callers evaluate this once at startup and combine it with
// OCIImageVolumeEnabled.
func ImageVolumeSupported(disco discovery.DiscoveryInterface) (bool, error) {
	v, err := disco.ServerVersion()
	if err != nil {
		return false, fmt.Errorf("reading server version: %w", err)
	}
	// Vendor builds suffix the minor with "+" (e.g. "33+" on GKE/EKS);
	// healthcheck.go's bare Atoi would fail on those.
	major, err := strconv.Atoi(strings.TrimRight(v.Major, "+"))
	if err != nil {
		return false, fmt.Errorf("parsing server major version %q: %w", v.Major, err)
	}
	minor, err := strconv.Atoi(strings.TrimRight(v.Minor, "+"))
	if err != nil {
		return false, fmt.Errorf("parsing server minor version %q: %w", v.Minor, err)
	}
	return major > 1 || (major == 1 && minor >= 33), nil
}
