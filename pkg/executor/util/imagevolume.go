// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/client-go/discovery"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// OCIImageVolumeName is the pod volume name used for OCI package image
// volumes (RFC-0001 Path B). Distinct from the fetcher's "userfunc" emptyDir
// so the two can coexist on newdeploy pods.
const OCIImageVolumeName = "oci-package-image"

// OCIVolumeReference is the image reference the kubelet pulls for a Path B
// volume. When the archive pins a digest, it is appended to the reference
// (repo:tag@sha256:...) so the kubelet enforces the pin — Path B has no
// fetcher to verify it. A reference that already carries a digest is used
// verbatim (OCIArchive.Validate rejects setting both).
func OCIVolumeReference(oa *fv1.OCIArchive) string {
	if oa.Digest == "" || strings.Contains(oa.Image, "@") {
		return oa.Image
	}
	return oa.Image + "@" + oa.Digest
}

// AddImageVolume appends an OCI image volume holding the package filesystem
// and mounts it read-only at mountPath on each named container. The volume
// reference embeds the archive's digest pin when set (OCIVolumeReference);
// pull secrets append to pod.Spec.ImagePullSecrets — the kubelet resolves
// image-volume pulls from those (plus the service account's). Call this
// AFTER every MergePodSpec so a runtime pod spec cannot strip or shadow the
// mount; it errors rather than silently producing a pod without the code
// mount (a missing mount would otherwise surface as a misleading
// "no such file" deep in the env container).
func AddImageVolume(podSpec *apiv1.PodSpec, oa *fv1.OCIArchive, mountPath string, containerNames ...string) error {
	for _, v := range podSpec.Volumes {
		if v.Name == OCIImageVolumeName {
			return fmt.Errorf("pod spec already has a volume named %q", OCIImageVolumeName)
		}
	}
	podSpec.Volumes = append(podSpec.Volumes, apiv1.Volume{
		Name: OCIImageVolumeName,
		VolumeSource: apiv1.VolumeSource{
			Image: &apiv1.ImageVolumeSource{
				Reference:  OCIVolumeReference(oa),
				PullPolicy: apiv1.PullIfNotPresent,
			},
		},
	})
	mount := apiv1.VolumeMount{
		Name:      OCIImageVolumeName,
		MountPath: mountPath,
		// Normalized for the volumeMount, which rejects absolute paths
		// ("" or "/" mean the image root); validation additionally rejects
		// unclean or traversing sub-paths at admission.
		SubPath:  strings.Trim(oa.SubPath, "/"),
		ReadOnly: true,
	}
	matched := 0
	for i := range podSpec.Containers {
		for _, name := range containerNames {
			if podSpec.Containers[i].Name == name {
				podSpec.Containers[i].VolumeMounts = append(podSpec.Containers[i].VolumeMounts, mount)
				matched++
			}
		}
	}
	if matched != len(containerNames) {
		return fmt.Errorf("image volume mount matched %d of %d containers %v", matched, len(containerNames), containerNames)
	}
	podSpec.ImagePullSecrets = append(podSpec.ImagePullSecrets, oa.ImagePullSecrets...)
	return nil
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

// ImageVolumeGate is the once-at-startup evaluation of the RFC-0001 Path B
// gate, shared by poolmgr and newdeploy so the two cannot drift: the
// operator's ENABLE_OCI_IMAGE_VOLUME opt-in combined with cluster support.
// A malformed opt-in value and a failed discovery probe are both loud — the
// operator asked for the feature, so silently running without it would turn
// one boot-time blip into an unexplained delivery-mode downgrade.
func ImageVolumeGate(logger logr.Logger, disco discovery.DiscoveryInterface) bool {
	raw := os.Getenv("ENABLE_OCI_IMAGE_VOLUME")
	if raw == "" {
		return false
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		logger.Error(err, "invalid ENABLE_OCI_IMAGE_VOLUME value; OCI image-volume delivery stays disabled", "value", raw)
		return false
	}
	if !enabled {
		return false
	}
	supported, err := ImageVolumeSupported(disco)
	if err != nil {
		logger.Error(err, "failed to check image-volume support; OCI packages stay on the fetcher path (support unknown, not absent)")
		return false
	}
	logger.Info("OCI image-volume delivery (RFC-0001 Path B)", "enabled", supported, "serverSupports", supported)
	return supported
}
