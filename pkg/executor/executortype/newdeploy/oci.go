// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// getFunctionOCIArchive returns the function's OCI deployment archive when
// image-volume delivery applies (RFC-0001 Path B), nil otherwise. Unlike
// poolmgr, newdeploy keeps the fetcher in the pod (it still materializes
// Secrets/ConfigMaps and drives the load), so the only conditions are the
// once-evaluated gate and the package actually being OCI. Errors fall back
// to the fetcher pull path rather than failing the rollout.
func (deploy *NewDeploy) getFunctionOCIArchive(ctx context.Context, fn *fv1.Function) *fv1.OCIArchive {
	if !deploy.imageVolumeOK {
		return nil
	}
	pkgRef := fn.Spec.Package.PackageRef
	pkg, err := deploy.fissionClient.CoreV1().Packages(pkgRef.Namespace).Get(ctx, pkgRef.Name, metav1.GetOptions{})
	if err != nil {
		deploy.logger.Error(err, "failed to read package for OCI eligibility; falling back to fetcher path",
			"package", pkgRef.Name, "namespace", pkgRef.Namespace, "function", fn.Name)
		return nil
	}
	return pkg.Spec.Deployment.OCI
}
