// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"k8s.io/client-go/discovery"
)

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
