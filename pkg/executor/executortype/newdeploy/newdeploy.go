// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	k8s_err "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util"
	"github.com/fission/fission/pkg/svcinfo"
	"github.com/fission/fission/pkg/utils"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

func (deploy *NewDeploy) createOrGetDeployment(ctx context.Context, fn *fv1.Function, env *fv1.Environment,
	deployName string, deployLabels map[string]string, deployAnnotations map[string]string, deployNamespace string) (*appsv1.Deployment, error) {

	specializationTimeout := fn.Spec.InvokeStrategy.ExecutionStrategy.SpecializationTimeout
	minScale := int32(fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale)

	// Always scale to at least one pod when createOrGetDeployment
	// is called. The idleObjectReaper will scale-in the deployment
	// later if no requests to the function.
	if minScale <= 0 {
		minScale = 1
	}

	deployment, err := deploy.getDeploymentSpec(ctx, fn, env, &minScale, deployName, deployNamespace, deployLabels, deployAnnotations)
	if err != nil {
		return nil, err
	}

	existingDepl, err := deploy.kubernetesClient.AppsV1().Deployments(deployNamespace).Get(ctx, deployName, metav1.GetOptions{})
	if err == nil {
		// Try to adopt orphan deployment created by the old executor.
		if existingDepl.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] != deploy.instanceID {
			existingDepl.Annotations = deployment.Annotations
			existingDepl.Labels = deployment.Labels
			existingDepl.OwnerReferences = deployment.OwnerReferences
			existingDepl.Spec.Template.Spec.Containers = deployment.Spec.Template.Spec.Containers
			existingDepl.Spec.Template.Spec.ServiceAccountName = deployment.Spec.Template.Spec.ServiceAccountName
			existingDepl.Spec.Template.Spec.TerminationGracePeriodSeconds = deployment.Spec.Template.Spec.TerminationGracePeriodSeconds

			// Update with the latest deployment spec. Kubernetes will trigger
			// rolling update if spec is different from the one in the cluster.
			existingDepl, err = deploy.kubernetesClient.AppsV1().Deployments(deployNamespace).Update(ctx, existingDepl, metav1.UpdateOptions{})
			if err != nil {
				deploy.logger.Error(err, "error adopting deploy", "deploy", deployName, "ns", deployNamespace)
				return nil, err
			}
			// In this case, we just return without waiting for it for fast bootstraping.
			return existingDepl, nil
		}

		if *existingDepl.Spec.Replicas < minScale {
			err = util.ScaleDeployment(ctx, deploy.kubernetesClient, deploy.logger, existingDepl.Namespace, existingDepl.Name, minScale)
			if err != nil {
				deploy.logger.Error(err, "error scaling up function deployment", "function", fn.Name)
				return nil, err
			}
		}
		if existingDepl.Status.AvailableReplicas < minScale {
			existingDepl, err = util.WaitForDeployment(ctx, deploy.kubernetesClient, deploy.logger, existingDepl, minScale, specializationTimeout)
		}

		return existingDepl, err
	} else if k8s_err.IsNotFound(err) {

		depl, err := deploy.kubernetesClient.AppsV1().Deployments(deployNamespace).Create(ctx, deployment, metav1.CreateOptions{})
		if err != nil {
			if k8s_err.IsAlreadyExists(err) {
				depl, err = deploy.kubernetesClient.AppsV1().Deployments(deployNamespace).Get(ctx, deployName, metav1.GetOptions{})
			}
			if err != nil {
				deploy.logger.Error(err, "error while creating function deployment", "function", fn.Name,
					"deployment_name", deployName,
					"deployment_namespace", deployNamespace)
				return nil, err
			}
		}
		if minScale > 0 {
			depl, err = util.WaitForDeployment(ctx, deploy.kubernetesClient, deploy.logger, depl, minScale, specializationTimeout)
		}
		return depl, err
	}
	return nil, err
}

func (deploy *NewDeploy) updateDeployment(ctx context.Context, deployment *appsv1.Deployment, ns string) error {
	_, err := deploy.kubernetesClient.AppsV1().Deployments(ns).Update(ctx, deployment, metav1.UpdateOptions{})
	return err
}

func (deploy *NewDeploy) deleteDeployment(ctx context.Context, ns string, name string) error {
	// DeletePropagationBackground deletes the object immediately and dependent are deleted later
	// DeletePropagationForeground not advisable; it marks for deletion and API can still serve those objects
	deletePropagation := metav1.DeletePropagationBackground
	return deploy.kubernetesClient.AppsV1().Deployments(ns).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &deletePropagation,
	})
}

func (deploy *NewDeploy) getDeploymentSpec(ctx context.Context, fn *fv1.Function, env *fv1.Environment, targetReplicas *int32,
	deployName string, deployNamespace string, deployLabels map[string]string, deployAnnotations map[string]string) (*appsv1.Deployment, error) {

	replicas := int32(fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale)
	if targetReplicas != nil {
		replicas = *targetReplicas
	}

	gracePeriodSeconds := int64(6 * 60)
	if env.Spec.TerminationGracePeriod >= 0 {
		gracePeriodSeconds = env.Spec.TerminationGracePeriod
	}

	podAnnotations := env.Annotations
	if podAnnotations == nil {
		podAnnotations = make(map[string]string)
	}

	// Here, we don't append deployAnnotations to podAnnotations
	// since newdeploy doesn't manager pod lifecycle directly.

	if deploy.useIstio && env.Spec.AllowAccessToExternalNetwork {
		podAnnotations["sidecar.istio.io/inject"] = "false"
	}

	podLabels := env.Labels
	if podLabels == nil {
		podLabels = make(map[string]string)
	}

	maps.Copy(podLabels, deployLabels)

	resources := deploy.getResources(env, fn)

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

	// Newdeploy updates the environment variable "LastUpdateTimestamp" of deployment
	// whenever a configmap/secret gets an update, but it also leaves multiple ReplicaSets for
	// rollback purpose. Since fission always update a deployment instead of performing a
	// rollback, set RevisionHistoryLimit to 0 to disable this feature.
	revisionHistoryLimit := int32(0)

	rvCount, err := util.ReferencedResourcesRVSum(ctx, deploy.kubernetesClient, fn.Namespace, fn.Spec.Secrets, fn.Spec.ConfigMaps)
	if err != nil {
		return nil, err
	}

	container, err := util.MergeContainer(&apiv1.Container{
		Name:                   env.Name,
		Image:                  env.Spec.Runtime.Image,
		ImagePullPolicy:        deploy.runtimeImagePullPolicy,
		TerminationMessagePath: "/dev/termination-log",
		// Connection-draining preStop hook; see utils.DrainLifecycle.
		Lifecycle: utils.DrainLifecycle(gracePeriodSeconds),
		Env: append([]apiv1.EnvVar{
			{
				Name:  fv1.ResourceVersionCount,
				Value: fmt.Sprintf("%d", rvCount),
			},
			// RFC-0023 state API env (nil when functionState is off). The token
			// itself arrives via the specialize-on-startup fetcher, which writes
			// it to the shared mount (see fetcher.StateTokenFileName).
		}, util.StateAPIEnvVars(deploy.fetcherConfig.SharedMountPath())...),
		// https://istio.io/docs/setup/kubernetes/additional-setup/requirements/
		Ports: []apiv1.ContainerPort{
			{
				Name: "http-env",
				// Now that we have added Port field in spec, should we make this configurable too?
				ContainerPort: int32(svcinfo.PortEnvRuntime),
			},
		},
		Resources: resources,
	}, env.Spec.Runtime.Container)
	if err != nil {
		return nil, err
	}

	// AutomountServiceAccountToken=false stops Kubernetes from injecting
	// the fission-fetcher ServiceAccount token into every container in
	// the pod. The fetcher container re-mounts the token via the
	// projected volume defined below — the user-code container does not.
	// See GHSA-85g2-pmrx-r49q.
	automountSAToken := false
	pod := apiv1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      podLabels,
			Annotations: podAnnotations,
		},
		Spec: apiv1.PodSpec{
			Containers:                    []apiv1.Container{*container},
			ServiceAccountName:            fv1.FissionFetcherSA,
			AutomountServiceAccountToken:  &automountSAToken,
			TerminationGracePeriodSeconds: &gracePeriodSeconds,
			Volumes: []apiv1.Volume{
				util.FetcherSATokenProjectedVolume(),
			},
		},
	}

	if deploy.podSpecPatch != nil {

		updatedPodSpec, err := util.MergePodSpec(&pod.Spec, deploy.podSpecPatch)
		if err == nil {
			pod.Spec = *updatedPodSpec
		} else {
			deploy.logger.Error(err, "Failed to merge the specs")
		}
		// Re-clamp after the merge: MergePodSpec propagates a non-nil
		// AutomountServiceAccountToken from the patch, which would otherwise
		// re-enable the kubelet auto-mount on the user container. See
		// GHSA-85g2-pmrx-r49q.
		pod.Spec.AutomountServiceAccountToken = new(false)
	}

	pod.Spec = *(util.ApplyImagePullSecret(env.Spec.ImagePullSecret, pod.Spec))

	var ownerReferences []metav1.OwnerReference
	if deploy.enableOwnerReferences {
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

	// If custom runtime container name - default env name
	mainContainerName, err := deploy.mainContainerName(env)
	if err != nil {
		return nil, err
	}

	// Order of merging is important here - first fetcher, then containers and lastly pod spec
	err = deploy.fetcherConfig.AddSpecializingFetcherToPodSpec(
		&deployment.Spec.Template.Spec,
		mainContainerName,
		deployNamespace,
		fn,
		env,
	)
	if err != nil {
		return nil, err
	}

	if env.Spec.Runtime.PodSpec != nil {
		newPodSpec, err := util.MergePodSpec(&deployment.Spec.Template.Spec, env.Spec.Runtime.PodSpec)
		if err != nil {
			return nil, err
		}
		deployment.Spec.Template.Spec = *newPodSpec
		// Re-clamp after the merge: MergePodSpec propagates a non-nil
		// AutomountServiceAccountToken from env.Spec.Runtime.PodSpec, which
		// would otherwise re-enable the kubelet auto-mount on the user
		// container. See GHSA-85g2-pmrx-r49q.
		deployment.Spec.Template.Spec.AutomountServiceAccountToken = new(false)
	}

	// Re-mount the fission-fetcher SA token at the canonical Kubernetes
	// path on the fetcher container only. The pod-level
	// AutomountServiceAccountToken=false flag set above suppresses the
	// implicit mount on every container, including fetcher, so we have
	// to add it back explicitly here. This must run AFTER the
	// env.Spec.Runtime.PodSpec merge — MergePodSpec can append additional
	// volumeMounts to the fetcher container, including one at this same
	// path, and kubelet would reject the pod with a duplicate-mount-path
	// error. The helper strips any pre-existing mount at the path before
	// adding its own, so running it last guarantees a single mount on the
	// fetcher container backed by the projected SA token volume. See
	// GHSA-85g2-pmrx-r49q.
	if err := util.MountFetcherSATokenOnFetcher(&deployment.Spec.Template.Spec); err != nil {
		return nil, err
	}

	// RFC-0001 Path B: mount the package image read-only at the fetcher's
	// store path on both containers. The fetcher's exists-early-exit then
	// skips the pull and proceeds straight to secrets + load — Path B for
	// newdeploy is delivery-only; the stock fetcher flow runs unchanged
	// (the early-exit makes the fetch a no-op). Applied AFTER every
	// MergePodSpec (same convention as the SA-token re-clamps) so a runtime
	// pod spec cannot strip or shadow the code mount.
	oci, err := deploy.getFunctionOCIArchive(ctx, fn)
	if err != nil {
		return nil, err
	}
	if oci != nil {
		if err := util.AddImageVolume(&deployment.Spec.Template.Spec, oci,
			filepath.Join(deploy.fetcherConfig.SharedMountPath(), deploy.fetcherConfig.TargetFilename(fn, env)),
			mainContainerName, util.FetcherContainerName); err != nil {
			return nil, err
		}
	}

	return deployment, nil
}

// mainContainerName resolves the name of the function's main (user-code)
// container in the deployment's pod spec. It defaults to the environment name
// and switches to the custom runtime container name when the environment
// declares one that is actually present in the runtime PodSpec. It is shared by
// the deployment-spec builder and the HPA call site so the ContainerResource
// metric can never name a container that differs from the one in the pod.
func (deploy *NewDeploy) mainContainerName(env *fv1.Environment) (string, error) {
	mainContainerName := env.Name
	if env.Spec.Runtime.Container != nil && env.Spec.Runtime.Container.Name != "" && env.Spec.Runtime.PodSpec != nil {
		if util.DoesContainerExistInPodSpec(env.Spec.Runtime.Container.Name, env.Spec.Runtime.PodSpec) {
			mainContainerName = env.Spec.Runtime.Container.Name
		} else {
			return "", fmt.Errorf("runtime container %s not found in pod spec", env.Spec.Runtime.Container.Name)
		}
	}
	return mainContainerName, nil
}

// getResources overrides only the resources which are overridden at function level otherwise
// default to resources specified at environment level
func (deploy *NewDeploy) getResources(env *fv1.Environment, fn *fv1.Function) apiv1.ResourceRequirements {
	resources := env.Spec.Resources
	if resources.Requests == nil {
		resources.Requests = make(map[apiv1.ResourceName]resource.Quantity)
	}
	if resources.Limits == nil {
		resources.Limits = make(map[apiv1.ResourceName]resource.Quantity)
	}
	// Only override the once specified at function, rest default to values from env.
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

func (deploy *NewDeploy) createOrGetSvc(ctx context.Context, fn *fv1.Function, deployLabels map[string]string, deployAnnotations map[string]string, svcName string, svcNamespace string) (*apiv1.Service, error) {
	logger := otelUtils.LoggerWithTraceID(ctx, deploy.logger)
	var ownerReferences []metav1.OwnerReference
	if deploy.enableOwnerReferences {
		ownerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(fn, fv1.SchemeGroupVersion.WithKind("Function")),
		}
	}

	// The Service carries the managed-by label (RFC-0002) so the EndpointSlice
	// controller mirrors it onto the slices and the router's label-filtered
	// informer sees them. Labels only — the selector stays deployLabels.
	svcLabels := make(map[string]string, len(deployLabels)+1)
	maps.Copy(svcLabels, deployLabels)
	svcLabels[fv1.MANAGED_BY_LABEL] = fv1.MANAGED_BY_VALUE

	service := &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            svcName,
			Labels:          svcLabels,
			Annotations:     deployAnnotations,
			OwnerReferences: ownerReferences,
		},
		Spec: apiv1.ServiceSpec{
			Ports: []apiv1.ServicePort{
				{
					Name: "http-env",
					Port: int32(80),
					// Since Function spec now supports Port , should we make this configurable too?
					TargetPort: intstr.FromInt(svcinfo.PortEnvRuntime),
				},
			},
			Selector: deployLabels,
			Type:     apiv1.ServiceTypeClusterIP,
		},
	}

	svc, created, err := util.CreateOrAdoptService(ctx, deploy.kubernetesClient, logger, deploy.instanceID, svcNamespace, service)
	if err != nil {
		return nil, err
	}
	if created {
		otelUtils.SpanTrackEvent(ctx, "createdService", otelUtils.GetAttributesForSvc(svc)...)
	}
	return svc, nil
}

func (deploy *NewDeploy) deleteSvc(ctx context.Context, ns string, name string) error {
	return deploy.kubernetesClient.CoreV1().Services(ns).Delete(ctx, name, metav1.DeleteOptions{})
}

// cleanupNewdeploy cleans all kubernetes objects related to function
func (deploy *NewDeploy) cleanupNewdeploy(ctx context.Context, ns string, name string) error {
	var result error

	err := deploy.deleteSvc(ctx, ns, name)
	if err != nil && !k8s_err.IsNotFound(err) {
		deploy.logger.Error(err, "error deleting service for newdeploy function", "function_name", name,
			"function_namespace", ns)
		result = errors.Join(result, err)
	}

	err = deploy.hpaops.DeleteHpa(ctx, ns, name)
	if err != nil && !k8s_err.IsNotFound(err) {
		deploy.logger.Error(err, "error deleting HPA for newdeploy function", "function_name", name,
			"function_namespace", ns)
		result = errors.Join(result, err)
	}

	err = deploy.deleteDeployment(ctx, ns, name)
	if err != nil && !k8s_err.IsNotFound(err) {
		deploy.logger.Error(err, "error deleting deployment for newdeploy function", "function_name", name,
			"function_namespace", ns)
		result = errors.Join(result, err)
	}

	return result
}
