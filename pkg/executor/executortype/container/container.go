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
	"strconv"
	"strings"
	"time"

	multierror "github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	asv1 "k8s.io/api/autoscaling/v1"
	apiv1 "k8s.io/api/core/v1"
	k8s_err "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util"
)

const (
	DeploymentKind    = "Deployment"
	DeploymentVersion = "apps/v1"
)

func (cn *Container) createOrGetDeployment(fn *fv1.Function, deployName string, deployLabels map[string]string, deployAnnotations map[string]string, deployNamespace string) (*appsv1.Deployment, error) {

	// The specializationTimeout here refers to the creation of the pod and not the loading of function
	// as in other executors.
	specializationTimeout := int(fn.Spec.InvokeStrategy.ExecutionStrategy.SpecializationTimeout)
	minScale := int32(fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale)

	// Always scale to at least one pod when createOrGetDeployment
	// is called. The idleObjectReaper will scale-in the deployment
	// later if no requests to the function.
	if minScale <= 0 {
		minScale = 1
	}

	deployment, err := cn.getDeploymentSpec(fn, &minScale, deployName, deployNamespace, deployLabels, deployAnnotations)
	if err != nil {
		return nil, err
	}

	existingDepl, err := cn.kubernetesClient.AppsV1().Deployments(deployNamespace).Get(context.TODO(), deployName, metav1.GetOptions{})
	if err == nil {
		// Try to adopt orphan deployment created by the old executor.
		if existingDepl.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] != cn.instanceID {
			existingDepl.Annotations = deployment.Annotations
			existingDepl.Labels = deployment.Labels
			existingDepl.Spec.Template.Spec.Containers = deployment.Spec.Template.Spec.Containers
			existingDepl.Spec.Template.Spec.ServiceAccountName = deployment.Spec.Template.Spec.ServiceAccountName
			existingDepl.Spec.Template.Spec.TerminationGracePeriodSeconds = deployment.Spec.Template.Spec.TerminationGracePeriodSeconds

			// Update with the latest deployment spec. Kubernetes will trigger
			// rolling update if spec is different from the one in the cluster.
			existingDepl, err = cn.kubernetesClient.AppsV1().Deployments(deployNamespace).Update(context.TODO(), existingDepl, metav1.UpdateOptions{})
			if err != nil {
				cn.logger.Warn("error adopting cn", zap.Error(err),
					zap.String("cn", deployName), zap.String("ns", deployNamespace))
				return nil, err
			}
			// In this case, we just return without waiting for it for fast bootstraping.
			return existingDepl, nil
		}

		if *existingDepl.Spec.Replicas < minScale {
			err = cn.scaleDeployment(existingDepl.Namespace, existingDepl.Name, minScale)
			if err != nil {
				cn.logger.Error("error scaling up function deployment", zap.Error(err), zap.String("function", fn.ObjectMeta.Name))
				return nil, err
			}
		}
		if existingDepl.Status.AvailableReplicas < minScale {
			existingDepl, err = cn.waitForDeploy(existingDepl, minScale, specializationTimeout)
		}

		return existingDepl, err
	} else if k8s_err.IsNotFound(err) {
		depl, err := cn.kubernetesClient.AppsV1().Deployments(deployNamespace).Create(context.TODO(), deployment, metav1.CreateOptions{})
		if err != nil {
			if k8s_err.IsAlreadyExists(err) {
				depl, err = cn.kubernetesClient.AppsV1().Deployments(deployNamespace).Get(context.TODO(), deployName, metav1.GetOptions{})
			}
			if err != nil {
				cn.logger.Error("error while creating function deployment",
					zap.Error(err),
					zap.String("function", fn.ObjectMeta.Name),
					zap.String("deployment_name", deployName),
					zap.String("deployment_namespace", deployNamespace))
				return nil, err
			}
		}
		if minScale > 0 {
			depl, err = cn.waitForDeploy(depl, minScale, specializationTimeout)
		}
		return depl, err
	}
	return nil, err
}

func (cn *Container) updateDeployment(deployment *appsv1.Deployment, ns string) error {
	_, err := cn.kubernetesClient.AppsV1().Deployments(ns).Update(context.TODO(), deployment, metav1.UpdateOptions{})
	return err
}

func (cn *Container) deleteDeployment(ns string, name string) error {
	// DeletePropagationBackground deletes the object immediately and dependent are deleted later
	// DeletePropagationForeground not advisable; it marks for deleteion and API can still serve those objects
	deletePropagation := metav1.DeletePropagationBackground
	return cn.kubernetesClient.AppsV1().Deployments(ns).Delete(context.TODO(), name, metav1.DeleteOptions{
		PropagationPolicy: &deletePropagation,
	})
}

func (cn *Container) getDeploymentSpec(fn *fv1.Function, targetReplicas *int32,
	deployName string, deployNamespace string, deployLabels map[string]string, deployAnnotations map[string]string) (*appsv1.Deployment, error) {

	var command, args []string
	replicas := int32(fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale)
	if targetReplicas != nil {
		replicas = *targetReplicas
	}

	gracePeriodSeconds := int64(6 * 60)

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
	envFromSources, err := util.ConvertConfigSecrets(fn, cn.kubernetesClient)
	if err != nil {
		return nil, err
	}

	rvCount, err := referencedResourcesRVSum(cn.kubernetesClient, fn.ObjectMeta.Namespace, fn.Spec.Secrets, fn.Spec.ConfigMaps)
	if err != nil {
		return nil, err
	}

	if fn.Spec.Command != "" {
		command = strings.Split(fn.Spec.Command, " ")
	}
	if fn.Spec.Args != "" {
		args = strings.Split(fn.Spec.Args, " ")
	}
	container := &apiv1.Container{
		Name:                   fn.ObjectMeta.Name,
		Image:                  fn.Spec.Image,
		Command:                command,
		Args:                   args,
		ImagePullPolicy:        cn.runtimeImagePullPolicy,
		TerminationMessagePath: "/dev/termination-log",
		Lifecycle: &apiv1.Lifecycle{
			PreStop: &apiv1.Handler{
				Exec: &apiv1.ExecAction{
					Command: []string{
						"/bin/sleep",
						fmt.Sprintf("%v", gracePeriodSeconds),
					},
				},
			},
		},
		Env: []apiv1.EnvVar{
			{
				Name:  fv1.ResourceVersionCount,
				Value: fmt.Sprintf("%v", rvCount),
			},
		},
		EnvFrom: envFromSources,
		// https://istio.io/docs/setup/kubernetes/additional-setup/requirements/
		Ports: []apiv1.ContainerPort{
			{
				Name:          "http-env",
				ContainerPort: int32(fn.Spec.Port),
			},
		},
		Resources: resources,
	}

	pod := apiv1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      podLabels,
			Annotations: podAnnotations,
		},
		Spec: apiv1.PodSpec{
			Containers:                    []apiv1.Container{*container},
			TerminationGracePeriodSeconds: &gracePeriodSeconds,
		},
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

func (cn *Container) createOrGetHpa(hpaName string, execStrategy *fv1.ExecutionStrategy,
	depl *appsv1.Deployment, deployLabels map[string]string, deployAnnotations map[string]string) (*asv1.HorizontalPodAutoscaler, error) {

	if depl == nil {
		return nil, errors.New("failed to create HPA, found empty deployment")
	}

	minRepl := int32(execStrategy.MinScale)
	if minRepl == 0 {
		minRepl = 1
	}
	maxRepl := int32(execStrategy.MaxScale)
	if maxRepl == 0 {
		maxRepl = minRepl
	}
	targetCPU := int32(execStrategy.TargetCPUPercent)

	hpa := &asv1.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:        hpaName,
			Labels:      deployLabels,
			Annotations: deployAnnotations,
		},
		Spec: asv1.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: asv1.CrossVersionObjectReference{
				Kind:       DeploymentKind,
				Name:       depl.ObjectMeta.Name,
				APIVersion: DeploymentVersion,
			},
			MinReplicas:                    &minRepl,
			MaxReplicas:                    maxRepl,
			TargetCPUUtilizationPercentage: &targetCPU,
		},
	}

	existingHpa, err := cn.kubernetesClient.AutoscalingV1().HorizontalPodAutoscalers(depl.ObjectMeta.Namespace).Get(context.TODO(), hpaName, metav1.GetOptions{})
	if err == nil {
		// to adopt orphan service
		if existingHpa.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] != cn.instanceID {
			existingHpa.Annotations = hpa.Annotations
			existingHpa.Labels = hpa.Labels
			existingHpa.Spec = hpa.Spec
			existingHpa, err = cn.kubernetesClient.AutoscalingV1().HorizontalPodAutoscalers(depl.ObjectMeta.Namespace).Update(context.TODO(), existingHpa, metav1.UpdateOptions{})
			if err != nil {
				cn.logger.Warn("error adopting HPA", zap.Error(err),
					zap.String("HPA", hpaName), zap.String("ns", depl.ObjectMeta.Namespace))
				return nil, err
			}
		}
		return existingHpa, err
	} else if k8s_err.IsNotFound(err) {
		cHpa, err := cn.kubernetesClient.AutoscalingV1().HorizontalPodAutoscalers(depl.ObjectMeta.Namespace).Create(context.TODO(), hpa, metav1.CreateOptions{})
		if err != nil {
			if k8s_err.IsAlreadyExists(err) {
				cHpa, err = cn.kubernetesClient.AutoscalingV1().HorizontalPodAutoscalers(depl.ObjectMeta.Namespace).Get(context.TODO(), hpaName, metav1.GetOptions{})
			}
			if err != nil {
				return nil, err
			}
		}
		return cHpa, nil
	}
	return nil, err
}

func (cn *Container) getHpa(ns, name string) (*asv1.HorizontalPodAutoscaler, error) {
	return cn.kubernetesClient.AutoscalingV1().HorizontalPodAutoscalers(ns).Get(context.TODO(), name, metav1.GetOptions{})
}

func (cn *Container) updateHpa(hpa *asv1.HorizontalPodAutoscaler) error {
	_, err := cn.kubernetesClient.AutoscalingV1().HorizontalPodAutoscalers(hpa.ObjectMeta.Namespace).Update(context.TODO(), hpa, metav1.UpdateOptions{})
	return err
}

func (cn *Container) deleteHpa(ns string, name string) error {
	return cn.kubernetesClient.AutoscalingV1().HorizontalPodAutoscalers(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})
}

func (cn *Container) createOrGetSvc(fn *fv1.Function, deployLabels map[string]string, deployAnnotations map[string]string, svcName string, svcNamespace string) (*apiv1.Service, error) {
	service := &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        svcName,
			Labels:      deployLabels,
			Annotations: deployAnnotations,
		},
		Spec: apiv1.ServiceSpec{
			Ports: []apiv1.ServicePort{
				{
					Name:       "http-env",
					Port:       int32(80),
					TargetPort: intstr.FromInt(fn.Spec.Port),
				},
			},
			Selector: deployLabels,
			Type:     apiv1.ServiceTypeClusterIP,
		},
	}

	existingSvc, err := cn.kubernetesClient.CoreV1().Services(svcNamespace).Get(context.TODO(), svcName, metav1.GetOptions{})
	if err == nil {
		// to adopt orphan service
		if existingSvc.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] != cn.instanceID {
			existingSvc.Annotations = service.Annotations
			existingSvc.Labels = service.Labels
			existingSvc.Spec.Ports = service.Spec.Ports
			existingSvc.Spec.Selector = service.Spec.Selector
			existingSvc.Spec.Type = service.Spec.Type
			existingSvc, err = cn.kubernetesClient.CoreV1().Services(svcNamespace).Update(context.TODO(), existingSvc, metav1.UpdateOptions{})
			if err != nil {
				cn.logger.Warn("error adopting service", zap.Error(err),
					zap.String("service", svcName), zap.String("ns", svcNamespace))
				return nil, err
			}
		}
		return existingSvc, err
	} else if k8s_err.IsNotFound(err) {
		svc, err := cn.kubernetesClient.CoreV1().Services(svcNamespace).Create(context.TODO(), service, metav1.CreateOptions{})
		if err != nil {
			if k8s_err.IsAlreadyExists(err) {
				svc, err = cn.kubernetesClient.CoreV1().Services(svcNamespace).Get(context.TODO(), svcName, metav1.GetOptions{})
			}
			if err != nil {
				return nil, err
			}
		}
		return svc, nil
	}
	return nil, err
}

func (cn *Container) deleteSvc(ns string, name string) error {
	return cn.kubernetesClient.CoreV1().Services(ns).Delete(context.TODO(), name, metav1.DeleteOptions{})
}

func (cn *Container) waitForDeploy(depl *appsv1.Deployment, replicas int32, specializationTimeout int) (*appsv1.Deployment, error) {
	// if no specializationTimeout is set, use default value
	if specializationTimeout < fv1.DefaultSpecializationTimeOut {
		specializationTimeout = fv1.DefaultSpecializationTimeOut
	}

	for i := 0; i < specializationTimeout; i++ {
		latestDepl, err := cn.kubernetesClient.AppsV1().Deployments(depl.ObjectMeta.Namespace).Get(context.TODO(), depl.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		// TODO check for imagePullerror
		// use AvailableReplicas here is better than ReadyReplicas
		// since the pods may not be able to serve network traffic yet.
		if latestDepl.Status.AvailableReplicas >= replicas {
			return latestDepl, err
		}
		time.Sleep(time.Second)
	}

	// this error appears in the executor pod logs
	timeoutError := fmt.Errorf("failed to create deployment within the timeout window of %d seconds", specializationTimeout)
	return nil, timeoutError
}

// cleanupContainer cleans all kubernetes objects related to function
func (cn *Container) cleanupContainer(ns string, name string) error {
	result := &multierror.Error{}

	err := cn.deleteSvc(ns, name)
	if err != nil && !k8s_err.IsNotFound(err) {
		cn.logger.Error("error deleting service for Container function",
			zap.Error(err),
			zap.String("function_name", name),
			zap.String("function_namespace", ns))
		result = multierror.Append(result, err)
	}

	err = cn.deleteHpa(ns, name)
	if err != nil && !k8s_err.IsNotFound(err) {
		cn.logger.Error("error deleting HPA for Container function",
			zap.Error(err),
			zap.String("function_name", name),
			zap.String("function_namespace", ns))
		result = multierror.Append(result, err)
	}

	err = cn.deleteDeployment(ns, name)
	if err != nil && !k8s_err.IsNotFound(err) {
		cn.logger.Error("error deleting deployment for Container function",
			zap.Error(err),
			zap.String("function_name", name),
			zap.String("function_namespace", ns))
		result = multierror.Append(result, err)
	}

	return result.ErrorOrNil()
}

// referencedResourcesRVSum returns the sum of resource version of all resources the function references to.
// We used to update timestamp in the deployment environment field in order to trigger a rolling update when
// the function referenced resources get updated. However, use timestamp means we are not able to avoid tri-
// ggering a rolling update when executor tries to adopt orphaned deployment due to timestamp changed which
// is unwanted. In order to let executor adopt deployment without triggering a rolling update, we need an
// identical way to get a value that can reflect resources changed without affecting by the time.
// To achieve this goal, the sum of the resource version of all referenced resources is a good fit for our
// scenario since the sum of the resource version is always the same as long as no resources changed.
func referencedResourcesRVSum(client *kubernetes.Clientset, namespace string, secrets []fv1.SecretReference, cfgmaps []fv1.ConfigMapReference) (int, error) {
	rvCount := 0

	if len(secrets) > 0 {
		list, err := client.CoreV1().Secrets(namespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return 0, err
		}

		objmap := make(map[string]apiv1.Secret)
		for _, secret := range list.Items {
			objmap[secret.Namespace+"/"+secret.Name] = secret
		}

		for _, ref := range secrets {
			s, ok := objmap[ref.Namespace+"/"+ref.Name]
			if ok {
				rv, _ := strconv.ParseInt(s.ResourceVersion, 10, 32)
				rvCount += int(rv)
			}
		}
	}

	if len(cfgmaps) > 0 {
		list, err := client.CoreV1().ConfigMaps(namespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return 0, err
		}

		objmap := make(map[string]apiv1.ConfigMap)
		for _, cfg := range list.Items {
			objmap[cfg.Namespace+"/"+cfg.Name] = cfg
		}

		for _, ref := range cfgmaps {
			s, ok := objmap[ref.Namespace+"/"+ref.Name]
			if ok {
				rv, _ := strconv.ParseInt(s.ResourceVersion, 10, 32)
				rvCount += int(rv)
			}
		}
	}

	return rvCount, nil
}
