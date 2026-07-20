// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"context"
	"fmt"
	"maps"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	k8s_err "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util"
	"github.com/fission/fission/pkg/utils"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

func (cn *Container) createOrGetDeployment(ctx context.Context, fn *fv1.Function, deployName string, deployLabels map[string]string, deployAnnotations map[string]string, deployNamespace string) (*appsv1.Deployment, error) {
	logger := otelUtils.LoggerWithTraceID(ctx, cn.logger)

	// The specializationTimeout here refers to the creation of the pod and not the loading of function
	// as in other executors.
	specializationTimeout := fn.Spec.InvokeStrategy.ExecutionStrategy.SpecializationTimeout
	minScale := int32(fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale)

	// Always scale to at least one pod when createOrGetDeployment
	// is called. The idleObjectReaper will scale-in the deployment
	// later if no requests to the function.
	if minScale <= 0 {
		minScale = 1
	}

	deployment, err := cn.getDeploymentSpec(ctx, fn, &minScale, deployName, deployNamespace, deployLabels, deployAnnotations)
	if err != nil {
		return nil, err
	}

	existingDepl, err := cn.kubernetesClient.AppsV1().Deployments(deployNamespace).Get(ctx, deployName, metav1.GetOptions{})
	if err != nil && !k8s_err.IsNotFound(err) {
		return nil, err
	}

	// Create new deployment if one does not previously exist
	if k8s_err.IsNotFound(err) {
		depl, err := cn.kubernetesClient.AppsV1().Deployments(deployNamespace).Create(ctx, deployment, metav1.CreateOptions{})
		if err != nil {
			if k8s_err.IsAlreadyExists(err) {
				depl, err = cn.kubernetesClient.AppsV1().Deployments(deployNamespace).Get(ctx, deployName, metav1.GetOptions{})
			}
			if err != nil {
				logger.Error(err, "error while creating function deployment", "function", fn.Name,
					"deployment_name", deployName,
					"deployment_namespace", deployNamespace)
				return nil, err
			}
		}
		otelUtils.SpanTrackEvent(ctx, "deploymentCreated", otelUtils.GetAttributesForDeployment(depl)...)
		if minScale > 0 {
			depl, err = util.WaitForDeployment(ctx, cn.kubernetesClient, cn.logger, depl, minScale, specializationTimeout)
		}
		return depl, err
	}

	// Try to adopt orphan deployment created by the old executor.
	if existingDepl.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] != cn.instanceID {
		existingDepl.Annotations = deployment.Annotations
		existingDepl.Labels = deployment.Labels
		existingDepl.OwnerReferences = deployment.OwnerReferences
		existingDepl.Spec.Template.Spec.Containers = deployment.Spec.Template.Spec.Containers
		existingDepl.Spec.Template.Spec.ServiceAccountName = deployment.Spec.Template.Spec.ServiceAccountName
		existingDepl.Spec.Template.Spec.TerminationGracePeriodSeconds = deployment.Spec.Template.Spec.TerminationGracePeriodSeconds

		// Update with the latest deployment spec. Kubernetes will trigger
		// rolling update if spec is different from the one in the cluster.
		existingDepl, err = cn.kubernetesClient.AppsV1().Deployments(deployNamespace).Update(ctx, existingDepl, metav1.UpdateOptions{})
		if err != nil {
			logger.Error(err, "error adopting cn", "cn", deployName, "ns", deployNamespace)
			return nil, err
		}
		// In this case, we just return without waiting for it for fast bootstraping.
		return existingDepl, nil
	}

	if *existingDepl.Spec.Replicas < minScale {
		err = util.ScaleDeployment(ctx, cn.kubernetesClient, cn.logger, existingDepl.Namespace, existingDepl.Name, minScale)
		if err != nil {
			logger.Error(err, "error scaling up function deployment", "function", fn.Name)
			return nil, err
		}
	}
	if existingDepl.Status.AvailableReplicas < minScale {
		existingDepl, err = util.WaitForDeployment(ctx, cn.kubernetesClient, cn.logger, existingDepl, minScale, specializationTimeout)
	}

	return existingDepl, err
}

func (cn *Container) updateDeployment(ctx context.Context, deployment *appsv1.Deployment, ns string) error {
	_, err := cn.kubernetesClient.AppsV1().Deployments(ns).Update(ctx, deployment, metav1.UpdateOptions{})
	return err
}

func (cn *Container) deleteDeployment(ctx context.Context, ns string, name string) error {
	// DeletePropagationBackground deletes the object immediately and dependent are deleted later
	// DeletePropagationForeground not advisable; it marks for deletion and API can still serve those objects
	deletePropagation := metav1.DeletePropagationBackground
	return cn.kubernetesClient.AppsV1().Deployments(ns).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &deletePropagation,
	})
}

func (cn *Container) getDeploymentSpec(ctx context.Context, fn *fv1.Function, targetReplicas *int32,
	deployName string, deployNamespace string, deployLabels map[string]string, deployAnnotations map[string]string) (*appsv1.Deployment, error) {

	replicas := int32(fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale)
	if targetReplicas != nil {
		replicas = *targetReplicas
	}

	if fn.Spec.PodSpec == nil {
		return nil, fmt.Errorf("podSpec is not set for function %s", fn.Name)
	}

	gracePeriodSeconds := int64(6 * 60)
	if fn.Spec.PodSpec.TerminationGracePeriodSeconds != nil && *fn.Spec.PodSpec.TerminationGracePeriodSeconds >= 0 {
		gracePeriodSeconds = *fn.Spec.PodSpec.TerminationGracePeriodSeconds
	}

	podAnnotations := make(map[string]string)

	if cn.useIstio {
		podAnnotations["sidecar.istio.io/inject"] = "false"
	}

	podLabels := make(map[string]string)

	maps.Copy(podLabels, deployLabels)

	// Set maxUnavailable and maxSurge to 20% is because we want
	// fission to rollout newer function version gradually without
	// affecting any online service. For example, if you set maxSurge
	// to 100%, the new ReplicaSet scales up immediately and may
	// consume all remaining compute resources which might be an
	// issue if a cluster's resource is on a budget.
	// TODO: add to ExecutionStrategy so that the user
	// can do more fine control over different functions.
	maxUnavailable := intstr.FromString("20%")
	maxSurge := intstr.FromString("20%")

	// Container updates the environment variable "LastUpdateTimestamp" of deployment
	// whenever a configmap/secret gets an update, but it also leaves multiple ReplicaSets for
	// rollback purpose. Since fission always update a deployment instead of performing a
	// rollback, set RevisionHistoryLimit to 0 to disable this feature.
	revisionHistoryLimit := int32(0)

	resources := cn.getResources(fn)

	// Other executor types rely on Environments to add configmaps and secrets
	envFromSources, err := util.ConvertConfigSecrets(ctx, fn, cn.kubernetesClient)
	if err != nil {
		return nil, err
	}

	rvCount, err := util.ReferencedResourcesRVSum(ctx, cn.kubernetesClient, fn.Namespace, fn.Spec.Secrets, fn.Spec.ConfigMaps)
	if err != nil {
		return nil, err
	}

	container := &apiv1.Container{
		Name:                   fn.Name,
		ImagePullPolicy:        cn.runtimeImagePullPolicy,
		TerminationMessagePath: "/dev/termination-log",
		// Connection-draining preStop hook; see utils.DrainLifecycle.
		Lifecycle: utils.DrainLifecycle(gracePeriodSeconds),
		Env: []apiv1.EnvVar{
			{
				Name:  fv1.ResourceVersionCount,
				Value: fmt.Sprintf("%d", rvCount),
			},
		},
		EnvFrom: envFromSources,
		// https://istio.io/docs/setup/kubernetes/additional-setup/requirements/
		Resources: resources,
	}
	podSpec, err := util.MergePodSpec(&apiv1.PodSpec{
		Containers:                    []apiv1.Container{*container},
		TerminationGracePeriodSeconds: &gracePeriodSeconds,
	}, fn.Spec.PodSpec)
	if err != nil {
		return nil, err
	}
	pod := apiv1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      podLabels,
			Annotations: podAnnotations,
		},
		Spec: *podSpec,
	}

	pod.Spec = *(util.ApplyImagePullSecret("", pod.Spec))

	var ownerReferences []metav1.OwnerReference
	if cn.enableOwnerReferences {
		ownerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(fn, fv1.SchemeGroupVersion.WithKind("Function")),
		}
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            deployName,
			Labels:          deployLabels,
			Annotations:     deployAnnotations,
			OwnerReferences: ownerReferences,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: deployLabels,
			},
			Template: pod,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &maxUnavailable,
					MaxSurge:       &maxSurge,
				},
			},
			RevisionHistoryLimit: &revisionHistoryLimit,
		},
	}

	return deployment, nil
}
