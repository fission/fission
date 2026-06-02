// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"context"
	"errors"

	apiv1 "k8s.io/api/core/v1"
	k8s_err "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// getResources gets the resources(CPU, memory) set for the function
func (cn *Container) getResources(fn *fv1.Function) apiv1.ResourceRequirements {
	resources := fn.Spec.Resources
	if resources.Requests == nil {
		resources.Requests = make(map[apiv1.ResourceName]resource.Quantity)
	}
	if resources.Limits == nil {
		resources.Limits = make(map[apiv1.ResourceName]resource.Quantity)
	}

	val, ok := fn.Spec.Resources.Requests[apiv1.ResourceCPU]
	if ok && !val.IsZero() {
		resources.Requests[apiv1.ResourceCPU] = fn.Spec.Resources.Requests[apiv1.ResourceCPU]
	}

	val, ok = fn.Spec.Resources.Requests[apiv1.ResourceMemory]
	if ok && !val.IsZero() {
		resources.Requests[apiv1.ResourceMemory] = fn.Spec.Resources.Requests[apiv1.ResourceMemory]
	}

	val, ok = fn.Spec.Resources.Limits[apiv1.ResourceCPU]
	if ok && !val.IsZero() {
		resources.Limits[apiv1.ResourceCPU] = fn.Spec.Resources.Limits[apiv1.ResourceCPU]
	}

	val, ok = fn.Spec.Resources.Limits[apiv1.ResourceMemory]
	if ok && !val.IsZero() {
		resources.Limits[apiv1.ResourceMemory] = fn.Spec.Resources.Limits[apiv1.ResourceMemory]
	}

	return resources
}

// cleanupContainer cleans all kubernetes objects related to function
func (cn *Container) cleanupContainer(ctx context.Context, ns string, name string) error {
	var result error

	err := cn.deleteSvc(ctx, ns, name)
	if err != nil && !k8s_err.IsNotFound(err) {
		cn.logger.Error(err, "error deleting service for Container function", "function_name", name,
			"function_namespace", ns)
		result = errors.Join(result, err)
	}

	err = cn.hpaops.DeleteHpa(ctx, ns, name)
	if err != nil && !k8s_err.IsNotFound(err) {
		cn.logger.Error(err, "error deleting HPA for Container function", "function_name", name,
			"function_namespace", ns)
		result = errors.Join(result, err)
	}

	err = cn.deleteDeployment(ctx, ns, name)
	if err != nil && !k8s_err.IsNotFound(err) {
		cn.logger.Error(err, "error deleting deployment for Container function", "function_name", name,
			"function_namespace", ns)
		result = errors.Join(result, err)
	}

	return result
}
