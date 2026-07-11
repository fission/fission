// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"

	apiv1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/svcinfo"
	"github.com/fission/fission/pkg/utils"
)

func getIstioServiceLabels(fnName string) map[string]string {
	return map[string]string{
		"functionName": fnName,
	}
}

// createIstioServiceForFunction creates the per-function ClusterIP Service that
// istio needs (istio only routes traffic through k8s services, so a poolmgr
// function — whose pod lives in the warm pool — needs a stable service in front
// of it). It is idempotent: an already-existing service is treated as success.
// Driven by the Function reconciler on create; replaces the old istio AddFunc
// handler.
func (gpm *GenericPoolManager) createIstioServiceForFunction(ctx context.Context, fn *fv1.Function) error {
	sel := map[string]string{
		"functionName": fn.Name,
		"functionUid":  string(fn.UID),
	}
	svcName := utils.GetFunctionIstioServiceName(fn.Name, fn.Namespace)
	envNs := gpm.nsResolver.GetFunctionNS(fn.Spec.Environment.Namespace)

	svc := apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: envNs,
			Name:      svcName,
			Labels:    getIstioServiceLabels(fn.Name),
		},
		Spec: apiv1.ServiceSpec{
			Type: apiv1.ServiceTypeClusterIP,
			Ports: []apiv1.ServicePort{
				// Service port name should begin with a recognized prefix, or the traffic will be
				// treated as TCP traffic. (https://istio.io/docs/setup/kubernetes/additional-setup/requirements/)
				{
					Name:       "http-fetcher",
					Protocol:   apiv1.ProtocolTCP,
					Port:       svcinfo.PortFetcher,
					TargetPort: intstr.FromInt(svcinfo.PortFetcher),
				},
				{
					Name:       "http-env",
					Protocol:   apiv1.ProtocolTCP,
					Port:       svcinfo.PortEnvRuntime,
					TargetPort: intstr.FromInt(svcinfo.PortEnvRuntime),
				},
			},
			Selector: sel,
		},
	}

	_, err := gpm.kubernetesClient.CoreV1().Services(envNs).Create(ctx, &svc, metav1.CreateOptions{})
	if err != nil && !kerrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// deleteIstioServiceForFunction removes the per-function istio Service. Idempotent
// (a missing service is success). Driven by the Function reconciler on delete;
// replaces the old istio DeleteFunc handler.
func (gpm *GenericPoolManager) deleteIstioServiceForFunction(ctx context.Context, fn *fv1.Function) error {
	envNs := gpm.nsResolver.GetFunctionNS(fn.Spec.Environment.Namespace)
	svcName := utils.GetFunctionIstioServiceName(fn.Name, fn.Namespace)
	err := gpm.kubernetesClient.CoreV1().Services(envNs).Delete(ctx, svcName, metav1.DeleteOptions{})
	if err != nil && !kerrors.IsNotFound(err) {
		return err
	}
	return nil
}

// refreshFuncPods deletes the function's specialized pods so the next request
// re-specializes a warm pod with the function's current package/config. The
// Function reconciler calls it on a spec change: poolmgr otherwise has no
// function-update path, so a stale specialized pod (old package) could keep being
// routed to until the idle reaper happens to recycle it.
func (gpm *GenericPoolManager) refreshFuncPods(ctx context.Context, fn *fv1.Function) error {
	return gpm.RefreshFuncPods(ctx, gpm.logger, *fn)
}
