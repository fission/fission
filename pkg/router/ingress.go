/*
Copyright 2016 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package router

import (
	"context"
	"maps"
	"os"
	"reflect"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/router/util"
)

var podNamespace string

func init() {
	podNamespace = os.Getenv("POD_NAMESPACE")
	if podNamespace == "" {
		podNamespace = "fission"
	}
}

func createIngress(ctx context.Context, logger logr.Logger, trigger *fv1.HTTPTrigger, kubeClient kubernetes.Interface) {
	if !trigger.Spec.CreateIngress {
		return
	}
	_, err := kubeClient.NetworkingV1().Ingresses(podNamespace).Create(ctx, util.GetIngressSpec(podNamespace, trigger), v1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		logger.Error(err, "failed to create ingress")
		return
	}
	logger.V(1).Info("created ingress successfully for trigger", "trigger", trigger.Name)
}

func deleteIngress(ctx context.Context, logger logr.Logger, trigger *fv1.HTTPTrigger, kubeClient kubernetes.Interface) {
	if !trigger.Spec.CreateIngress {
		return
	}

	ingress, err := kubeClient.NetworkingV1().Ingresses(podNamespace).Get(ctx, trigger.Name, v1.GetOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		logger.Error(err, "failed to get ingress when deleting trigger", "trigger", trigger.Name)
		return
	}

	err = kubeClient.NetworkingV1().Ingresses(podNamespace).Delete(ctx, ingress.Name, v1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		logger.Error(err, "failed to delete ingress for trigger", "ingress", ingress,
			"trigger", trigger.Name)
	}
}

func updateIngress(ctx context.Context, logger logr.Logger, oldT *fv1.HTTPTrigger, newT *fv1.HTTPTrigger, kubeClient kubernetes.Interface) {
	if !oldT.Spec.CreateIngress && !newT.Spec.CreateIngress {
		return
	}

	if !oldT.Spec.CreateIngress && newT.Spec.CreateIngress {
		createIngress(ctx, logger, newT, kubeClient)
		return
	}

	if !newT.Spec.CreateIngress && oldT.Spec.CreateIngress {
		deleteIngress(ctx, logger, oldT, kubeClient)
		return
	}

	oldIngress, err := kubeClient.NetworkingV1().Ingresses(podNamespace).Get(ctx, oldT.Name, v1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			createIngress(ctx, logger, newT, kubeClient)
		}
		logger.Error(err, "failed to get ingress when updating trigger", "trigger", oldT.Name)
		return
	}
	newIngress := util.GetIngressSpec(podNamespace, newT)

	changes := false

	if !reflect.DeepEqual(oldIngress.Annotations, newIngress.Annotations) {
		logger.V(1).Info("ingress annotation",
			"old_trigger", oldIngress.Annotations, "new_trigger", newIngress.Annotations)

		if oldIngress.Annotations == nil || newIngress.Annotations == nil {
			oldIngress.Annotations = newIngress.Annotations
		} else {
			maps.Copy(oldIngress.Annotations, newIngress.Annotations)
		}
		changes = true
	}

	if !reflect.DeepEqual(oldIngress.Spec, newIngress.Spec) {
		logger.V(1).Info("ingress spec",
			"old_trigger", oldIngress.Spec, "new_trigger", newIngress.Spec)

		oldIngress.Spec = newIngress.Spec
		changes = true
	}

	if changes {
		_, err = kubeClient.NetworkingV1().Ingresses(podNamespace).Update(ctx, oldIngress, v1.UpdateOptions{})
		if err != nil {
			logger.Error(err, "failed to update ingress for trigger", "trigger", oldT.Name)
			return
		}

		logger.V(1).Info("updated ingress successfully for trigger",
			"old_trigger", oldT.Name, "new_trigger", newT.Name)
	}
}
