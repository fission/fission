// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import (
	"context"
	"fmt"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// getFunctionOCIArchive returns the function's OCI deployment archive when
// image-volume delivery applies (RFC-0001 Path B), (nil, nil) otherwise.
// Unlike poolmgr, newdeploy keeps the fetcher in the pod (it still
// materializes Secrets/ConfigMaps and drives the load), so the only
// conditions are the once-evaluated gate and the package actually being OCI.
// A deleted package falls back to the fetcher path (which reports it
// precisely); any other read failure is returned so the reconcile fails and
// requeues — silently dropping the image volume here would roll every
// function pod onto a degraded spec and report success.
func (deploy *NewDeploy) getFunctionOCIArchive(ctx context.Context, fn *fv1.Function) (*fv1.OCIArchive, error) {
	if !deploy.imageVolumeOK {
		return nil, nil
	}
	pkgRef := fn.Spec.Package.PackageRef
	pkg, err := deploy.fissionClient.CoreV1().Packages(pkgRef.Namespace).Get(ctx, pkgRef.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading package %s/%s for OCI eligibility of function %s: %w",
			pkgRef.Namespace, pkgRef.Name, fn.Name, err)
	}
	return pkg.Spec.Deployment.OCI, nil
}
