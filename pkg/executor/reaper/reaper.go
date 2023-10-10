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

package reaper

import (
	"context"
	"strings"

	"go.uber.org/zap"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
)

var (
	deletePropagation = metav1.DeletePropagationBackground
	delOpt            = metav1.DeleteOptions{PropagationPolicy: &deletePropagation}
)

// CleanupKubeObject deletes given kubernetes object
func CleanupKubeObject(ctx context.Context, logger *zap.Logger, kubeClient kubernetes.Interface, kubeobj *apiv1.ObjectReference) {
	switch strings.ToLower(kubeobj.Kind) {
	case "pod":
		err := kubeClient.CoreV1().Pods(kubeobj.Namespace).Delete(ctx, kubeobj.Name, metav1.DeleteOptions{})
		if err != nil {
			logger.Error("error cleaning up pod", zap.Error(err), zap.String("pod", kubeobj.Name))
		}

	case "service":
		err := kubeClient.CoreV1().Services(kubeobj.Namespace).Delete(ctx, kubeobj.Name, metav1.DeleteOptions{})
		if err != nil {
			logger.Error("error cleaning up service", zap.Error(err), zap.String("service", kubeobj.Name))
		}

	case "deployment":
		err := kubeClient.AppsV1().Deployments(kubeobj.Namespace).Delete(ctx, kubeobj.Name, delOpt)
		if err != nil {
			logger.Error("error cleaning up deployment", zap.Error(err), zap.String("deployment", kubeobj.Name))
		}

	case "horizontalpodautoscaler":
		err := kubeClient.AutoscalingV2().HorizontalPodAutoscalers(kubeobj.Namespace).Delete(ctx, kubeobj.Name, metav1.DeleteOptions{})
		if err != nil {
			logger.Error("error cleaning up horizontalpodautoscaler", zap.Error(err), zap.String("horizontalpodautoscaler", kubeobj.Name))
		}

	default:
		logger.Error("Could not identifying the object type to clean up", zap.String("type", kubeobj.Kind), zap.Any("object", kubeobj))

	}
}

// CleanupDeployments deletes deployment(s) for a given instanceID
func CleanupDeployments(ctx context.Context, logger *zap.Logger, client kubernetes.Interface, instanceID string, listOps metav1.ListOptions) error {
	cleanupDeployments := func(namespace string) error {
		deploymentList, err := client.AppsV1().Deployments(namespace).List(ctx, listOps)
		if err != nil {
			return err
		}
		for _, dep := range deploymentList.Items {
			id, ok := dep.ObjectMeta.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL]
			if !ok {
				// Backward compatibility with older label name
				id, ok = dep.ObjectMeta.Labels[fv1.EXECUTOR_INSTANCEID_LABEL]
			}
			if ok && id != instanceID {
				logger.Info("cleaning up deployment", zap.String("deployment", dep.ObjectMeta.Name))
				err := client.AppsV1().Deployments(dep.ObjectMeta.Namespace).Delete(ctx, dep.ObjectMeta.Name, delOpt)
				if err != nil {
					logger.Error("error cleaning up deployment",
						zap.Error(err),
						zap.String("deployment_name", dep.ObjectMeta.Name),
						zap.String("deployment_namespace", dep.ObjectMeta.Namespace))
				}
				// ignore err
			}
		}
		return nil
	}
	for _, namespace := range GetReaperNamespace() {
		if err := cleanupDeployments(namespace); err != nil {
			return err
		}
	}

	return nil
}

// CleanupPods deletes pod(s) for a given instanceID
func CleanupPods(ctx context.Context, logger *zap.Logger, client kubernetes.Interface, instanceID string, listOps metav1.ListOptions) error {
	cleanupPods := func(namespace string) error {
		podList, err := client.CoreV1().Pods(namespace).List(ctx, listOps)
		if err != nil {
			return err
		}
		for _, pod := range podList.Items {
			id, ok := pod.ObjectMeta.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL]
			if !ok {
				// Backward compatibility with older label name
				id, ok = pod.ObjectMeta.Labels[fv1.EXECUTOR_INSTANCEID_LABEL]
			}
			if ok && id != instanceID {
				logger.Info("cleaning up pod", zap.String("pod", pod.ObjectMeta.Name))
				err := client.CoreV1().Pods(pod.ObjectMeta.Namespace).Delete(ctx, pod.ObjectMeta.Name, metav1.DeleteOptions{})
				if err != nil {
					logger.Error("error cleaning up pod",
						zap.Error(err),
						zap.String("pod_name", pod.ObjectMeta.Name),
						zap.String("pod_namespace", pod.ObjectMeta.Namespace))
				}
				// ignore err
			}
		}
		return nil
	}

	for _, namespace := range GetReaperNamespace() {
		if err := cleanupPods(namespace); err != nil {
			return err
		}
	}

	return nil
}

// CleanupServices deletes service(s) for a given instanceID
func CleanupServices(ctx context.Context, logger *zap.Logger, client kubernetes.Interface, instanceID string, listOps metav1.ListOptions) error {
	cleanupServices := func(namespace string) error {
		svcList, err := client.CoreV1().Services(namespace).List(ctx, listOps)
		if err != nil {
			return err
		}
		for _, svc := range svcList.Items {
			id, ok := svc.ObjectMeta.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL]
			if !ok {
				// Backward compatibility with older label name
				id, ok = svc.ObjectMeta.Labels[fv1.EXECUTOR_INSTANCEID_LABEL]
			}
			if ok && id != instanceID {
				logger.Info("cleaning up service", zap.String("service", svc.ObjectMeta.Name))
				err := client.CoreV1().Services(svc.ObjectMeta.Namespace).Delete(ctx, svc.ObjectMeta.Name, metav1.DeleteOptions{})
				if err != nil {
					logger.Error("error cleaning up service",
						zap.Error(err),
						zap.String("service_name", svc.ObjectMeta.Name),
						zap.String("service_namespace", svc.ObjectMeta.Namespace))
				}
				// ignore err
			}
		}
		return nil
	}

	for _, namespace := range GetReaperNamespace() {
		if err := cleanupServices(namespace); err != nil {
			return err
		}
	}

	return nil
}

// CleanupHpa deletes horizontal pod autoscaler(s) for a given instanceID
func CleanupHpa(ctx context.Context, logger *zap.Logger, client kubernetes.Interface, instanceID string, listOps metav1.ListOptions) error {
	cleanupHpa := func(namespace string) error {
		hpaList, err := client.AutoscalingV2().HorizontalPodAutoscalers(namespace).List(ctx, listOps)
		if err != nil {
			return err
		}

		for _, hpa := range hpaList.Items {
			id, ok := hpa.ObjectMeta.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL]
			if !ok {
				// Backward compatibility with older label name
				id, ok = hpa.ObjectMeta.Labels[fv1.EXECUTOR_INSTANCEID_LABEL]
			}
			if ok && id != instanceID {
				logger.Info("cleaning up HPA", zap.String("hpa", hpa.ObjectMeta.Name))
				err := client.AutoscalingV2().HorizontalPodAutoscalers(hpa.ObjectMeta.Namespace).Delete(ctx, hpa.ObjectMeta.Name, metav1.DeleteOptions{})
				if err != nil {
					logger.Error("error cleaning up HPA",
						zap.Error(err),
						zap.String("hpa_name", hpa.ObjectMeta.Name),
						zap.String("hpa_namespace", hpa.ObjectMeta.Namespace))
				}
				// ignore err
			}
		}
		return nil
	}

	for _, namespace := range GetReaperNamespace() {
		if err := cleanupHpa(namespace); err != nil {
			return err
		}
	}

	return nil
}

func GetReaperNamespace() map[string]string {
	ns := utils.DefaultNSResolver()
	//to support backward compatibility we need to cleanup deployment and rolebinding created in function, buidler and default namespace as well
	fissionResourceNs := ns.FissionNSWithOptions(utils.WithBuilderNs(), utils.WithFunctionNs(), utils.WithDefaultNs())
	return fissionResourceNs
}
