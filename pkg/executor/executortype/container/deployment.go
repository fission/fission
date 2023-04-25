/*
Copyright 2020 The Fission Authors.

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

package container

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	apiv1 "k8s.io/api/core/v1"
	k8s_err "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util"
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
				logger.Error("error while creating function deployment",
					zap.Error(err),
					zap.String("function", fn.ObjectMeta.Name),
					zap.String("deployment_name", deployName),
					zap.String("deployment_namespace", deployNamespace))
				return nil, err
			}
		}
		otelUtils.SpanTrackEvent(ctx, "deploymentCreated", otelUtils.GetAttributesForDeployment(depl)...)
		if minScale > 0 {
			depl, err = cn.waitForDeploy(ctx, depl, minScale, specializationTimeout)
		}
		return depl, err
	}

	// Try to adopt orphan deployment created by the old executor.
	if existingDepl.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] != cn.instanceID {
		existingDepl.Annotations = deployment.Annotations
		existingDepl.Labels = deployment.Labels
		existingDepl.Spec.Template.Spec.Containers = deployment.Spec.Template.Spec.Containers
		existingDepl.Spec.Template.Spec.ServiceAccountName = deployment.Spec.Template.Spec.ServiceAccountName
		existingDepl.Spec.Template.Spec.TerminationGracePeriodSeconds = deployment.Spec.Template.Spec.TerminationGracePeriodSeconds

		// Update with the latest deployment spec. Kubernetes will trigger
		// rolling update if spec is different from the one in the cluster.
		existingDepl, err = cn.kubernetesClient.AppsV1().Deployments(deployNamespace).Update(ctx, existingDepl, metav1.UpdateOptions{})
		if err != nil {
			logger.Warn("error adopting cn", zap.Error(err),
				zap.String("cn", deployName), zap.String("ns", deployNamespace))
			return nil, err
		}
		// In this case, we just return without waiting for it for fast bootstraping.
		return existingDepl, nil
	}

	if *existingDepl.Spec.Replicas < minScale {
		err = cn.scaleDeployment(ctx, existingDepl.Namespace, existingDepl.Name, minScale)
		if err != nil {
			logger.Error("error scaling up function deployment", zap.Error(err), zap.String("function", fn.ObjectMeta.Name))
			return nil, err
		}
	}
	if existingDepl.Status.AvailableReplicas < minScale {
		existingDepl, err = cn.waitForDeploy(ctx, existingDepl, minScale, specializationTimeout)
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

func (cn *Container) waitForDeploy(ctx context.Context, depl *appsv1.Deployment, replicas int32, specializationTimeout int) (latestDepl *appsv1.Deployment, err error) {
	oldStatus := depl.Status
	otelUtils.SpanTrackEvent(ctx, "waitForDeployment", otelUtils.GetAttributesForDeployment(depl)...)
	// if no specializationTimeout is set, use default value
	if specializationTimeout < fv1.DefaultSpecializationTimeOut {
		specializationTimeout = fv1.DefaultSpecializationTimeOut
	}

	for i := 0; i < specializationTimeout; i++ {
		latestDepl, err = cn.kubernetesClient.AppsV1().Deployments(depl.ObjectMeta.Namespace).Get(ctx, depl.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		// TODO check for imagePullerror
		// use AvailableReplicas here is better than ReadyReplicas
		// since the pods may not be able to serve network traffic yet.
		if latestDepl.Status.AvailableReplicas >= replicas {
			otelUtils.SpanTrackEvent(ctx, "deploymentAvailable", otelUtils.GetAttributesForDeployment(latestDepl)...)
			return latestDepl, err
		}
		time.Sleep(time.Second)
	}

	logger := otelUtils.LoggerWithTraceID(ctx, cn.logger)
	logger.Error("Deployment provision failed within timeout window",
		zap.String("name", latestDepl.Name), zap.Any("old_status", oldStatus),
		zap.Any("current_status", latestDepl.Status), zap.Int("timeout", specializationTimeout))

	// this error appears in the executor pod logs
	timeoutError := fmt.Errorf("failed to create deployment within the timeout window of %d seconds", specializationTimeout)
	return nil, timeoutError
}

func (cn *Container) getDeploymentSpec(ctx context.Context, fn *fv1.Function, targetReplicas *int32,
	deployName string, deployNamespace string, deployLabels map[string]string, deployAnnotations map[string]string,
) (*appsv1.Deployment, error) {
	replicas := int32(fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale)
	if targetReplicas != nil {
		replicas = *targetReplicas
	}

	gracePeriodSeconds := int64(6 * 60)
	if *fn.Spec.PodSpec.TerminationGracePeriodSeconds >= 0 {
		gracePeriodSeconds = *fn.Spec.PodSpec.TerminationGracePeriodSeconds
	}

	podAnnotations := make(map[string]string)

	if cn.useIstio {
		podAnnotations["sidecar.istio.io/inject"] = "false"
	}

	podLabels := make(map[string]string)

	for k, v := range deployLabels {
		podLabels[k] = v
	}

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

	rvCount, err := referencedResourcesRVSum(ctx, cn.kubernetesClient, fn.ObjectMeta.Namespace, fn.Spec.Secrets, fn.Spec.ConfigMaps)
	if err != nil {
		return nil, err
	}

	if fn.Spec.PodSpec == nil {
		return nil, fmt.Errorf("podSpec is not set for function %s", fn.ObjectMeta.Name)
	}

	container := &apiv1.Container{
		Name:                   fn.ObjectMeta.Name,
		ImagePullPolicy:        cn.runtimeImagePullPolicy,
		TerminationMessagePath: "/dev/termination-log",
		// if the pod is specialized (i.e. has secrets), wait 60 seconds for the routers endpoint cache to expire before shutting down
		Lifecycle: &apiv1.Lifecycle{
			PreStop: &apiv1.LifecycleHandler{
				Exec: &apiv1.ExecAction{
					Command: []string{
						"bash",
						"-c",
						"test $(ls /secrets/) && sleep 63 || exit 0",
					},
				},
			},
		},
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

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        deployName,
			Labels:      deployLabels,
			Annotations: deployAnnotations,
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

func (caaf *Container) scaleDeployment(ctx context.Context, deplNS string, deplName string, replicas int32) error {
	caaf.logger.Info("scaling deployment",
		zap.String("deployment", deplName),
		zap.String("namespace", deplNS),
		zap.Int32("replicas", replicas))
	_, err := caaf.kubernetesClient.AppsV1().Deployments(deplNS).UpdateScale(ctx, deplName, &autoscalingv1.Scale{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deplName,
			Namespace: deplNS,
		},
		Spec: autoscalingv1.ScaleSpec{
			Replicas: replicas,
		},
	}, metav1.UpdateOptions{})
	return err
}
