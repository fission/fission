// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package reaper

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apiv1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
)

// CleanupExecutorObjects reaps the HorizontalPodAutoscaler, Deployment and
// Service objects of the given executor type whose executorInstanceId annotation
// no longer matches instanceID (orphans left by a previous executor instance).
// It's the shared body of the newdeploy and container managers'
// CleanupOldExecutorObjects; errors are logged and swallowed, matching the
// best-effort reaping the callers previously did inline.
func CleanupExecutorObjects(ctx context.Context, logger logr.Logger, client kubernetes.Interface, instanceID string, executorType fv1.ExecutorType) {
	logger.Info("starting to clean orphaned executor resources", "executorType", executorType, "instanceID", instanceID)
	listOpts := metav1.ListOptions{
		LabelSelector: labels.Set{fv1.EXECUTOR_TYPE: string(executorType)}.AsSelector().String(),
	}
	errs := errors.Join(
		CleanupHpa(ctx, logger, client, instanceID, listOpts),
		CleanupDeployments(ctx, logger, client, instanceID, listOpts),
		CleanupServices(ctx, logger, client, instanceID, listOpts),
	)
	if errs != nil {
		// TODO retry reaper; logged and ignored for now
		logger.Error(errs, "failed to cleanup old executor objects", "executorType", executorType)
	}
}

var (
	deletePropagation = metav1.DeletePropagationBackground
	delOpt            = metav1.DeleteOptions{PropagationPolicy: &deletePropagation}
)

// CleanupKubeObject deletes given kubernetes object, logging (not returning)
// any failure. Callers that need to retry use DeleteKubeObject directly.
func CleanupKubeObject(ctx context.Context, logger logr.Logger, kubeClient kubernetes.Interface, kubeobj *apiv1.ObjectReference) {
	if err := DeleteKubeObject(ctx, kubeClient, kubeobj); err != nil {
		logger.Error(err, "error cleaning up kubernetes object", "type", kubeobj.Kind, "name", kubeobj.Name, "ns", kubeobj.Namespace)
	}
}

// DeleteKubeObject deletes the given kubernetes object and reports the
// outcome: nil on success or already-gone, the API error otherwise.
func DeleteKubeObject(ctx context.Context, kubeClient kubernetes.Interface, kubeobj *apiv1.ObjectReference) error {
	var err error
	switch strings.ToLower(kubeobj.Kind) {
	case "pod":
		err = kubeClient.CoreV1().Pods(kubeobj.Namespace).Delete(ctx, kubeobj.Name, metav1.DeleteOptions{})
	case "service":
		err = kubeClient.CoreV1().Services(kubeobj.Namespace).Delete(ctx, kubeobj.Name, metav1.DeleteOptions{})
	case "deployment":
		err = kubeClient.AppsV1().Deployments(kubeobj.Namespace).Delete(ctx, kubeobj.Name, delOpt)
	case "horizontalpodautoscaler":
		err = kubeClient.AutoscalingV2().HorizontalPodAutoscalers(kubeobj.Namespace).Delete(ctx, kubeobj.Name, metav1.DeleteOptions{})
	default:
		return fmt.Errorf("could not identify the object type %q to clean up object %q", kubeobj.Kind, kubeobj.Name)
	}
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}
	return nil
}

// CleanupDeployments deletes deployment(s) for a given instanceID
func CleanupDeployments(ctx context.Context, logger logr.Logger, client kubernetes.Interface, instanceID string, listOps metav1.ListOptions) error {
	cleanupDeployments := func(namespace string) error {
		deploymentList, err := client.AppsV1().Deployments(namespace).List(ctx, listOps)
		if err != nil {
			return err
		}
		for _, dep := range deploymentList.Items {
			id, ok := dep.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL]
			if !ok {
				// Backward compatibility with older label name
				id, ok = dep.Labels[fv1.EXECUTOR_INSTANCEID_LABEL]
			}
			if ok && id != instanceID {
				logger.Info("cleaning up deployment", "deployment", dep.Name)
				err := client.AppsV1().Deployments(dep.ObjectMeta.Namespace).Delete(ctx, dep.Name, delOpt)
				if err != nil {
					logger.Error(err, "error cleaning up deployment", "deployment_name", dep.Name,
						"deployment_namespace", dep.Namespace)
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
func CleanupPods(ctx context.Context, logger logr.Logger, client kubernetes.Interface, instanceID string, listOps metav1.ListOptions) error {
	cleanupPods := func(namespace string) error {
		podList, err := client.CoreV1().Pods(namespace).List(ctx, listOps)
		if err != nil {
			return err
		}
		for _, pod := range podList.Items {
			id, ok := pod.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL]
			if !ok {
				// Backward compatibility with older label name
				id, ok = pod.Labels[fv1.EXECUTOR_INSTANCEID_LABEL]
			}
			if ok && id != instanceID {
				logger.Info("cleaning up pod", "pod", pod.Name)
				err := client.CoreV1().Pods(pod.ObjectMeta.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
				if err != nil {
					logger.Error(err, "error cleaning up pod", "pod_name", pod.Name,
						"pod_namespace", pod.Namespace)
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
func CleanupServices(ctx context.Context, logger logr.Logger, client kubernetes.Interface, instanceID string, listOps metav1.ListOptions) error {
	cleanupServices := func(namespace string) error {
		svcList, err := client.CoreV1().Services(namespace).List(ctx, listOps)
		if err != nil {
			return err
		}
		for _, svc := range svcList.Items {
			id, ok := svc.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL]
			if !ok {
				// Backward compatibility with older label name
				id, ok = svc.Labels[fv1.EXECUTOR_INSTANCEID_LABEL]
			}
			if ok && id != instanceID {
				logger.Info("cleaning up service", "service", svc.Name)
				err := client.CoreV1().Services(svc.ObjectMeta.Namespace).Delete(ctx, svc.Name, metav1.DeleteOptions{})
				if err != nil {
					logger.Error(err, "error cleaning up service", "service_name", svc.Name,
						"service_namespace", svc.Namespace)
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
func CleanupHpa(ctx context.Context, logger logr.Logger, client kubernetes.Interface, instanceID string, listOps metav1.ListOptions) error {
	cleanupHpa := func(namespace string) error {
		hpaList, err := client.AutoscalingV2().HorizontalPodAutoscalers(namespace).List(ctx, listOps)
		if err != nil {
			return err
		}

		for _, hpa := range hpaList.Items {
			id, ok := hpa.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL]
			if !ok {
				// Backward compatibility with older label name
				id, ok = hpa.Labels[fv1.EXECUTOR_INSTANCEID_LABEL]
			}
			if ok && id != instanceID {
				logger.Info("cleaning up HPA", "hpa", hpa.Name)
				err := client.AutoscalingV2().HorizontalPodAutoscalers(hpa.ObjectMeta.Namespace).Delete(ctx, hpa.Name, metav1.DeleteOptions{})
				if err != nil {
					logger.Error(err, "error cleaning up HPA", "hpa_name", hpa.Name,
						"hpa_namespace", hpa.Namespace)
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
	// to support backward compatibility we need to cleanup deployment and rolebinding created in function, buidler and default namespace as well
	fissionResourceNs := ns.FissionNSWithOptions(utils.WithBuilderNs(), utils.WithFunctionNs(), utils.WithDefaultNs())
	return fissionResourceNs
}
