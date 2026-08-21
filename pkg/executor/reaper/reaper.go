// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package reaper

import (
	"context"
	"errors"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	asv2 "k8s.io/api/autoscaling/v2"
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

// ownedLister is the slice of a typed client cleanupOrphaned needs; the
// namespaced accessors of every kubernetes.Interface group satisfy it.
type ownedLister[T any, L any] interface {
	List(ctx context.Context, opts metav1.ListOptions) (*L, error)
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
}

// cleanupOrphaned deletes, across every reaper namespace, each object whose
// executor-instanceID annotation (or, for backward compatibility, label)
// names a DIFFERENT executor instance — i.e. resources orphaned by a previous
// executor. Delete failures are logged and skipped, matching the historical
// per-kind loops: a leftover object must not abort the sweep. noun names the
// kind in the "cleaning up" message; its lowercase form prefixes the
// structured log fields (the two differ only for HPA, whose message says
// "HPA" but logs "hpa_name").
func cleanupOrphaned[T any, L any, PT interface {
	*T
	metav1.Object
}, C ownedLister[T, L]](ctx context.Context, logger logr.Logger, client func(ns string) C, fromList func(*L) []T, noun, instanceID string, listOps metav1.ListOptions, delOpts metav1.DeleteOptions) error {
	key := strings.ToLower(noun)
	clean := func(namespace string) error {
		list, err := client(namespace).List(ctx, listOps)
		if err != nil {
			return err
		}
		for _, item := range fromList(list) {
			obj := PT(&item)
			id, ok := obj.GetAnnotations()[fv1.EXECUTOR_INSTANCEID_LABEL]
			if !ok {
				// Backward compatibility with older label name
				id, ok = obj.GetLabels()[fv1.EXECUTOR_INSTANCEID_LABEL]
			}
			if ok && id != instanceID {
				logger.Info("cleaning up "+noun, key, obj.GetName())
				err := client(obj.GetNamespace()).Delete(ctx, obj.GetName(), delOpts)
				if err != nil {
					logger.Error(err, "error cleaning up "+noun, key+"_name", obj.GetName(),
						key+"_namespace", obj.GetNamespace())
				}
				// ignore err
			}
		}
		return nil
	}
	for _, namespace := range GetReaperNamespace() {
		if err := clean(namespace); err != nil {
			return err
		}
	}
	return nil
}

// CleanupDeployments deletes deployment(s) for a given instanceID
func CleanupDeployments(ctx context.Context, logger logr.Logger, client kubernetes.Interface, instanceID string, listOps metav1.ListOptions) error {
	return cleanupOrphaned(ctx, logger, client.AppsV1().Deployments,
		func(l *appsv1.DeploymentList) []appsv1.Deployment { return l.Items },
		"deployment", instanceID, listOps, delOpt)
}

// CleanupPods deletes pod(s) for a given instanceID
func CleanupPods(ctx context.Context, logger logr.Logger, client kubernetes.Interface, instanceID string, listOps metav1.ListOptions) error {
	return cleanupOrphaned(ctx, logger, client.CoreV1().Pods,
		func(l *apiv1.PodList) []apiv1.Pod { return l.Items },
		"pod", instanceID, listOps, metav1.DeleteOptions{})
}

// CleanupServices deletes service(s) for a given instanceID
func CleanupServices(ctx context.Context, logger logr.Logger, client kubernetes.Interface, instanceID string, listOps metav1.ListOptions) error {
	return cleanupOrphaned(ctx, logger, client.CoreV1().Services,
		func(l *apiv1.ServiceList) []apiv1.Service { return l.Items },
		"service", instanceID, listOps, metav1.DeleteOptions{})
}

// CleanupHpa deletes horizontal pod autoscaler(s) for a given instanceID
func CleanupHpa(ctx context.Context, logger logr.Logger, client kubernetes.Interface, instanceID string, listOps metav1.ListOptions) error {
	return cleanupOrphaned(ctx, logger, client.AutoscalingV2().HorizontalPodAutoscalers,
		func(l *asv2.HorizontalPodAutoscalerList) []asv2.HorizontalPodAutoscaler { return l.Items },
		"HPA", instanceID, listOps, metav1.DeleteOptions{})
}

func GetReaperNamespace() map[string]string {
	ns := utils.DefaultNSResolver()
	// to support backward compatibility we need to cleanup deployment and rolebinding created in function, buidler and default namespace as well
	fissionResourceNs := ns.FissionNSWithOptions(utils.WithBuilderNs(), utils.WithFunctionNs(), utils.WithDefaultNs())
	return fissionResourceNs
}
