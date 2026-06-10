// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sTypes "k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	executorUtils "github.com/fission/fission/pkg/executor/util"
)

// ociPoolHash derives the short stable hash that keys per-image pools and
// labels their pods (RFC-0001 Path B). It covers every archive field the
// pool's pod spec depends on — reference+digest, sub-path, pull secrets —
// so two packages that would produce different pods can never alias to the
// same pool (e.g. one image holding several functions under different
// sub-paths). nil -> empty hash -> the plain, fetcher-based pool.
func ociPoolHash(oa *fv1.OCIArchive) string {
	if oa == nil {
		return ""
	}
	parts := []string{executorUtils.OCIVolumeReference(oa), oa.SubPath}
	for _, s := range oa.ImagePullSecrets {
		parts = append(parts, s.Name)
	}
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
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
// function is eligible for image-volume delivery (Path B), (nil, nil) when it
// must use the plain fetcher pool (Path A or non-OCI). Ineligible: env v1
// (its store path is "user" and loadOnlySpecialize speaks only
// /v2/specialize), AllowedFunctionsPerContainerInfinite envs (their store
// path is per-function, which one shared mount cannot serve), and functions
// with Secrets/ConfigMaps (materialized by the fetcher, absent from Path B
// pods). A deleted package falls back to Path A — the fetcher reports the
// missing package with a precise error — but any other read failure is
// returned so the cold start fails visibly and the router retries, instead
// of silently pinning the function to the wrong pool type in fsCache. Runs
// on the cold path only (pool lookup), so the direct Package read is
// acceptable.
func (gpm *GenericPoolManager) getFunctionOCIArchive(ctx context.Context, fn *fv1.Function, env *fv1.Environment) (*fv1.OCIArchive, error) {
	if env.Spec.Version < 2 {
		return nil, nil
	}
	if env.Spec.AllowedFunctionsPerContainer == fv1.AllowedFunctionsPerContainerInfinite {
		return nil, nil
	}
	if len(fn.Spec.Secrets) > 0 || len(fn.Spec.ConfigMaps) > 0 {
		return nil, nil
	}
	pkgRef := fn.Spec.Package.PackageRef
	pkg, err := gpm.fissionClient.CoreV1().Packages(pkgRef.Namespace).Get(ctx, pkgRef.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return pkg.Spec.Deployment.OCI, nil
}
