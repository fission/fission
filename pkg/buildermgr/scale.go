// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"context"

	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes"

	"github.com/fission/fission/pkg/executor/util"
)

// deploymentScaler scales a deployment to a replica count via the scale
// subresource. It is injected into the PackageReconciler (scale up on demand)
// and the idle reaper (scale to zero) so unit tests can stub the write: the
// client-go fake clientset does not implement the scale subresource.
type deploymentScaler func(ctx context.Context, ns, name string, replicas int32) error

// k8sDeploymentScaler is the production scaler, backed by util.ScaleDeployment.
func k8sDeploymentScaler(kubernetesClient kubernetes.Interface, logger logr.Logger) deploymentScaler {
	return func(ctx context.Context, ns, name string, replicas int32) error {
		return util.ScaleDeployment(ctx, kubernetesClient, logger, ns, name, replicas)
	}
}
