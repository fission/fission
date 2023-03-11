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

package poolmgr

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	k8sErrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util"
)

// getPoolName returns a unique name of an environment
func getPoolName(env *fv1.Environment) string {
	// TODO: get rid of resource version here
	var envPodName string

	min := func(a, b int) int {
		if a > b {
			return b
		}
		return a
	}

	//To fit the 63 character limit
	if len(env.ObjectMeta.Name)+len(env.ObjectMeta.Namespace) < 37 {
		envPodName = env.ObjectMeta.Name + "-" + env.ObjectMeta.Namespace
	} else {
		nameLength := min(len(env.ObjectMeta.Name), 18)
		namespaceLength := min(len(env.ObjectMeta.Namespace), 18)
		envPodName = env.ObjectMeta.Name[:nameLength] + "-" + env.ObjectMeta.Namespace[:namespaceLength]
	}

	return "poolmgr-" + strings.ToLower(fmt.Sprintf("%s-%s", envPodName, env.ResourceVersion))
}

func (gp *GenericPool) genDeploymentMeta(env *fv1.Environment) metav1.ObjectMeta {
	deployLabels := gp.getEnvironmentPoolLabels(env)
	deployAnnotations := gp.getDeployAnnotations(env)
	return metav1.ObjectMeta{
		Name:        getPoolName(env),
		Labels:      deployLabels,
		Annotations: deployAnnotations,
	}
}

func (gp *GenericPool) genDeploymentSpec(env *fv1.Environment) (*appsv1.DeploymentSpec, error) {
	deployLabels := gp.getEnvironmentPoolLabels(env)
	// Use long terminationGracePeriodSeconds for connection draining in case that
	// pod still runs user functions.
	gracePeriodSeconds := int64(6 * 60)
	if env.Spec.TerminationGracePeriod >= 0 {
		gracePeriodSeconds = env.Spec.TerminationGracePeriod
	}

	podAnnotations := env.ObjectMeta.Annotations
	if podAnnotations == nil {
		podAnnotations = make(map[string]string)
	}

	// Here, we don't append executor instance-id to pod annotations
	// to prevent unwanted rolling updates occur. Pool manager will
	// append executor instance-id to pod annotations when a pod is chosen
	// for function specialization.

	if gp.useIstio && env.Spec.AllowAccessToExternalNetwork {
		podAnnotations["sidecar.istio.io/inject"] = "false"
	}

	podLabels := env.ObjectMeta.Labels
	if podLabels == nil {
		podLabels = make(map[string]string)
	}

	for k, v := range deployLabels {
		podLabels[k] = v
	}

	container, err := util.MergeContainer(&apiv1.Container{
		Name:                   env.ObjectMeta.Name,
		Image:                  env.Spec.Runtime.Image,
		ImagePullPolicy:        gp.runtimeImagePullPolicy,
		TerminationMessagePath: "/dev/termination-log",
		Resources:              env.Spec.Resources,
		// Pod is removed from endpoints list for service when it's
		// state became "Termination". We used preStop hook as the
		// workaround for connection draining since pod maybe shutdown
		// before grace period expires.
		// https://kubernetes.io/docs/concepts/workloads/pods/pod/#termination-of-pods
		// https://github.com/kubernetes/kubernetes/issues/47576#issuecomment-308900172
		Lifecycle: &apiv1.Lifecycle{
			PreStop: &apiv1.LifecycleHandler{
				Exec: &apiv1.ExecAction{
					Command: []string{
						"/bin/sleep",
						fmt.Sprintf("%v", gracePeriodSeconds),
					},
				},
			},
		},
		// https://istio.io/docs/setup/kubernetes/additional-setup/requirements/
		Ports: []apiv1.ContainerPort{
			{
				Name:          "http-fetcher",
				ContainerPort: int32(8000),
			},
			{
				Name:          "http-env",
				ContainerPort: int32(8888),
			},
		},
	}, env.Spec.Runtime.Container)
	if err != nil {
		return nil, err
	}

	pod := apiv1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      podLabels,
			Annotations: podAnnotations,
		},
		Spec: apiv1.PodSpec{
			Containers:         []apiv1.Container{*container},
			ServiceAccountName: fv1.FissionFetcherSA,
			// TerminationGracePeriodSeconds should be equal to the
			// sleep time of preStop to make sure that SIGTERM is sent
			// to pod after 6 mins.
			TerminationGracePeriodSeconds: &gracePeriodSeconds,
		},
	}

	if gp.podSpecPatch != nil {

		updatedPodSpec, err := util.MergePodSpec(&pod.Spec, gp.podSpecPatch)
		if err == nil {
			pod.Spec = *updatedPodSpec
		} else {
			gp.logger.Warn("Failed to merge the specs: %v", zap.Error(err))
		}
	}

	pod.Spec = *(util.ApplyImagePullSecret(env.Spec.ImagePullSecret, pod.Spec))

	poolsize := getEnvPoolSize(env)
	switch env.Spec.AllowedFunctionsPerContainer {
	case fv1.AllowedFunctionsPerContainerInfinite:
		poolsize = 1
	}

	deploymentSpec := appsv1.DeploymentSpec{
		// TODO: fix this hardcoded value
		Replicas: &poolsize,
		Selector: &metav1.LabelSelector{
			MatchLabels: deployLabels,
		},
		Template: pod,
	}

	// Order of merging is important here - first fetcher, then containers and lastly pod spec
	err = gp.fetcherConfig.AddFetcherToPodSpec(&deploymentSpec.Template.Spec, env.ObjectMeta.Name)
	if err != nil {
		return nil, err
	}

	if env.Spec.Runtime.PodSpec != nil {
		newPodSpec, err := util.MergePodSpec(&deploymentSpec.Template.Spec, env.Spec.Runtime.PodSpec)
		if err != nil {
			return nil, err
		}
		deploymentSpec.Template.Spec = *newPodSpec
	}
	return &deploymentSpec, nil
}

// A pool is a deployment of generic containers for an env.  This
// creates the pool but doesn't wait for any pods to be ready.
func (gp *GenericPool) createPoolDeployment(ctx context.Context, env *fv1.Environment) error {
	deploymentMeta := gp.genDeploymentMeta(env)
	deploymentSpec, err := gp.genDeploymentSpec(env)
	if err != nil {
		return err
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: deploymentMeta,
		Spec:       *deploymentSpec,
	}
	depl, err := gp.kubernetesClient.AppsV1().Deployments(gp.fnNamespace).Get(ctx, deployment.Name, metav1.GetOptions{})
	if err == nil {
		if depl.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] != gp.instanceID {
			deployment.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] = gp.instanceID
			// Update with the latest deployment spec. Kubernetes will trigger
			// rolling update if spec is different from the one in the cluster.
			depl, err = gp.kubernetesClient.AppsV1().Deployments(gp.fnNamespace).Update(ctx, deployment, metav1.UpdateOptions{})
		}
		gp.deployment = depl
		return err
	} else if !k8sErrs.IsNotFound(err) {
		gp.logger.Error("error getting deployment in kubernetes", zap.Error(err), zap.String("deployment", deployment.Name))
		return err
	}

	depl, err = gp.kubernetesClient.AppsV1().Deployments(gp.fnNamespace).Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil {
		gp.logger.Error("error creating deployment in kubernetes", zap.Error(err), zap.String("deployment", deployment.Name))
		return err
	}

	gp.deployment = depl
	gp.logger.Info("deployment created", zap.String("deployment", depl.Name), zap.String("ns", depl.Namespace), zap.Any("environment", env))

	return nil
}

func (gp *GenericPool) updatePoolDeployment(ctx context.Context, env *fv1.Environment) error {
	logger := gp.logger.With(zap.String("env", env.Name), zap.String("namespace", env.Namespace))
	if gp.env.ObjectMeta.ResourceVersion == env.ObjectMeta.ResourceVersion {
		logger.Debug("env resource version matching with pool env")
		return nil
	}
	newDeployment := gp.deployment.DeepCopy()
	spec, err := gp.genDeploymentSpec(env)
	if err != nil {
		logger.Error("error generating deployment spec", zap.Error(err))
		return err
	}
	newDeployment.Spec = *spec
	deployMeta := gp.genDeploymentMeta(env)
	deployMeta.Name = gp.deployment.Name
	newDeployment.ObjectMeta = deployMeta

	poolsize := getEnvPoolSize(env)
	switch env.Spec.AllowedFunctionsPerContainer {
	case fv1.AllowedFunctionsPerContainerInfinite:
		poolsize = 1
	}
	newDeployment.Spec.Replicas = &poolsize

	depl, err := gp.kubernetesClient.AppsV1().Deployments(gp.fnNamespace).Update(ctx, newDeployment, metav1.UpdateOptions{})
	if err != nil {
		logger.Error("error updating deployment in kubernetes", zap.Error(err), zap.String("deployment", depl.Name))
		return err
	}
	// possible concurrency issue here as
	// gp.env and gp.deployment referenced at few places
	// we can move update pool to gpm.service if required
	gp.env = env
	gp.deployment = depl
	logger.Info("Updated deployment for pool", zap.String("deployment", depl.Name))
	return nil
}
