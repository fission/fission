// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// ociImageHash derives the short stable hash that keys per-image pools and
// labels their pods (RFC-0001 Path B). Empty image -> empty hash -> the
// plain, fetcher-based pool.
func ociImageHash(image string) string {
	if image == "" {
		return ""
	}
	h := sha256.Sum256([]byte(image))
	return hex.EncodeToString(h[:])[:16]
}

// poolKey identifies a pool: the env UID alone for plain pools — byte-for-byte
// the pre-Path-B key, so non-OCI behavior is unchanged — or env UID + "/" +
// image hash for per-image pools.
func poolKey(envUID k8sTypes.UID, imageHash string) string {
	if imageHash == "" {
		return string(envUID)
	}
	return string(envUID) + "/" + imageHash
}

// envPoolKeyPrefixMatch reports whether key belongs to env: its plain pool or
// any of its per-image pools.
func envPoolKeyPrefixMatch(key string, envUID k8sTypes.UID) bool {
	return key == string(envUID) || strings.HasPrefix(key, string(envUID)+"/")
}

// getFunctionOCIArchive returns the function's OCI deployment archive when the
// function is eligible for image-volume delivery (Path B), nil otherwise —
// nil means the plain pool serves it via the fetcher (Path A). Ineligible:
// env v1 (its loader needs the fetcher's /specialize relay), functions with
// Secrets/ConfigMaps (materialized by the fetcher, absent from Path B pods),
// and non-OCI packages. Runs on the cold path only (pool lookup), so the
// direct Package read is acceptable.
func (gpm *GenericPoolManager) getFunctionOCIArchive(ctx context.Context, fn *fv1.Function, env *fv1.Environment) *fv1.OCIArchive {
	if env.Spec.Version < 2 {
		return nil
	}
	// Infinite-functions-per-container envs store each function's code at a
	// per-function path (the function UID); one shared image mount at the
	// fixed deployarchive path cannot satisfy that — keep them on the
	// fetcher path.
	if env.Spec.AllowedFunctionsPerContainer == fv1.AllowedFunctionsPerContainerInfinite {
		return nil
	}
	if len(fn.Spec.Secrets) > 0 || len(fn.Spec.ConfigMaps) > 0 {
		return nil
	}
	pkgRef := fn.Spec.Package.PackageRef
	pkg, err := gpm.fissionClient.CoreV1().Packages(pkgRef.Namespace).Get(ctx, pkgRef.Name, metav1.GetOptions{})
	if err != nil {
		gpm.logger.Error(err, "failed to read package for OCI eligibility; falling back to fetcher path",
			"package", pkgRef.Name, "namespace", pkgRef.Namespace, "function", fn.Name)
		return nil
	}
	return pkg.Spec.Deployment.OCI
}
