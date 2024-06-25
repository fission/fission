/*
Copyright 2017 The Fission Authors.

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

package buildermgr

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/manager"
)

const (
	LABEL_ENV_NAME            = "envName"
	LABEL_ENV_NAMESPACE       = "envNamespace"
	LABEL_ENV_RESOURCEVERSION = "envResourceVersion"
	LABEL_DEPLOYMENT_OWNER    = "owner"
	BUILDER_MGR               = "buildermgr"
)

var (
	deletePropagation = metav1.DeletePropagationBackground
	delOpt            = metav1.DeleteOptions{PropagationPolicy: &deletePropagation}
)

type (
	builderInfo struct {
		envMetadata *metav1.ObjectMeta
		deployment  *appsv1.Deployment
		service     *apiv1.Service
	}

	environmentWatcher struct {
		logger                 *zap.Logger
		cache                  map[types.UID]*builderInfo
		fissionClient          versioned.Interface
		kubernetesClient       kubernetes.Interface
		nsResolver             *utils.NamespaceResolver
		fetcherConfig          *fetcherConfig.Config
		builderImagePullPolicy apiv1.PullPolicy
		useIstio               bool
		podSpecPatch           *apiv1.PodSpec
		envWatchInformer       map[string]k8sCache.SharedIndexInformer
	}
)

func makeEnvironmentWatcher(
	ctx context.Context,
	logger *zap.Logger,
	fissionClient versioned.Interface,
	kubernetesClient kubernetes.Interface,
	fetcherConfig *fetcherConfig.Config,
	podSpecPatch *apiv1.PodSpec) (*environmentWatcher, error) {

	useIstio := false
	enableIstio := os.Getenv("ENABLE_ISTIO")
	if len(enableIstio) > 0 {
		istio, err := strconv.ParseBool(enableIstio)
		if err != nil {
			logger.Error("Failed to parse ENABLE_ISTIO, defaults to false")
		}
		useIstio = istio
	}

	builderImagePullPolicy := utils.GetImagePullPolicy(os.Getenv("BUILDER_IMAGE_PULL_POLICY"))

	envWatcher := &environmentWatcher{
		logger:                 logger.Named("environment_watcher"),
		cache:                  make(map[types.UID]*builderInfo),
		fissionClient:          fissionClient,
		kubernetesClient:       kubernetesClient,
		nsResolver:             utils.DefaultNSResolver(),
		builderImagePullPolicy: builderImagePullPolicy,
		useIstio:               useIstio,
		fetcherConfig:          fetcherConfig,
		podSpecPatch:           podSpecPatch,
		envWatchInformer:       utils.GetInformersForNamespaces(fissionClient, time.Minute*30, fv1.EnvironmentResource),
	}

	err := envWatcher.EnvWatchEventHandlers(ctx)
	if err != nil {
		return nil, err
	}
	return envWatcher, nil
}

func (env *environmentWatcher) getDeploymentLabels(envName string) map[string]string {
	return map[string]string{
		LABEL_DEPLOYMENT_OWNER: BUILDER_MGR,
		LABEL_ENV_NAME:         envName,
	}
}

func (envw *environmentWatcher) getLabels(envName string, envNamespace string, envResourceVersion string) map[string]string {
	return map[string]string{
		LABEL_ENV_NAME:            envName,
		LABEL_ENV_NAMESPACE:       envNamespace,
		LABEL_ENV_RESOURCEVERSION: envResourceVersion,
		LABEL_DEPLOYMENT_OWNER:    BUILDER_MGR,
	}
}

func (envw *environmentWatcher) Run(ctx context.Context, mgr manager.Interface) {
	mgr.AddInformers(ctx, envw.envWatchInformer)
}

func (envw *environmentWatcher) EnvWatchEventHandlers(ctx context.Context) error {
	for _, informer := range envw.envWatchInformer {
		_, err := informer.AddEventHandler(k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				envObj := obj.(*fv1.Environment)
				envw.AddUpdateBuilder(ctx, envObj)
			},
			UpdateFunc: func(oldObj interface{}, newObj interface{}) {
				oldEnvObj := oldObj.(*fv1.Environment)
				newEnvObj := newObj.(*fv1.Environment)
				if oldEnvObj.ObjectMeta.ResourceVersion != newEnvObj.ObjectMeta.ResourceVersion {
					envw.AddUpdateBuilder(ctx, newEnvObj)
				}
			},
			DeleteFunc: func(obj interface{}) {
				envObj := obj.(*fv1.Environment)
				envw.DeleteBuilder(ctx, envObj)
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (envw *environmentWatcher) AddUpdateBuilder(ctx context.Context, env *fv1.Environment) {
	// builder is not supported with v1 interface and ignore env without builder image
	if env.Spec.Version != 1 && len(env.Spec.Builder.Image) != 0 {
		if _, ok := envw.cache[crd.CacheKeyUIDFromMeta(&env.ObjectMeta)]; !ok {
			builderInfo, err := envw.createBuilder(ctx, env, envw.nsResolver.GetBuilderNS(env.ObjectMeta.Namespace))
			if err != nil {
				envw.logger.Error("error creating builder service", zap.Error(err))
				return
			}
			envw.cache[crd.CacheKeyUIDFromMeta(&env.ObjectMeta)] = builderInfo
		} else {
			envw.DeleteBuilder(ctx, env)
			// once older builder deleted then add new builder service
			builderInfo, err := envw.createBuilder(ctx, env, envw.nsResolver.GetBuilderNS(env.ObjectMeta.Namespace))
			if err != nil {
				envw.logger.Error("error updating builder service", zap.Error(err))
				return
			}
			envw.cache[crd.CacheKeyUIDFromMeta(&env.ObjectMeta)] = builderInfo
		}
	}
}

func (envw *environmentWatcher) DeleteBuilder(ctx context.Context, env *fv1.Environment) {
	if _, ok := envw.cache[crd.CacheKeyUIDFromMeta(&env.ObjectMeta)]; ok {
		envw.DeleteBuilderService(ctx, env)
		envw.DeleteBuilderDeployment(ctx, env)
		delete(envw.cache, crd.CacheKeyUIDFromMeta(&env.ObjectMeta))
		envw.logger.Info("builder service deleted", zap.String("env_name", env.ObjectMeta.Name), zap.String("namespace", envw.nsResolver.GetBuilderNS(env.ObjectMeta.Namespace)))
	} else {
		envw.logger.Debug("builder service not found", zap.String("env_name", env.ObjectMeta.Name), zap.String("namespace", envw.nsResolver.GetBuilderNS(env.ObjectMeta.Namespace)))
	}
}

func (envw *environmentWatcher) DeleteBuilderService(ctx context.Context, env *fv1.Environment) {
	ns := envw.nsResolver.GetBuilderNS(env.ObjectMeta.Namespace)
	svcList, err := envw.getBuilderServiceList(ctx, envw.getDeploymentLabels(env.ObjectMeta.Name), ns)
	if err != nil {
		envw.logger.Error("error getting the builder service list", zap.Error(err))
	}
	for _, svc := range svcList {
		envName := svc.ObjectMeta.Labels[LABEL_ENV_NAME]
		if _, ok := envw.cache[crd.CacheKeyUIDFromMeta(&env.ObjectMeta)]; ok {
			err := envw.deleteBuilderServiceByName(ctx, svc.ObjectMeta.Name, svc.ObjectMeta.Namespace)
			if err != nil {
				envw.logger.Error("error removing builder service", zap.Error(err),
					zap.String("service_name", svc.ObjectMeta.Name),
					zap.String("service_namespace", svc.ObjectMeta.Namespace),
					zap.String("env_name", envName))
			}
			break
		} else {
			envw.logger.Error("builder service not found",
				zap.String("service_name", svc.ObjectMeta.Name),
				zap.String("service_namespace", svc.ObjectMeta.Namespace))
		}
	}
}

func (envw *environmentWatcher) DeleteBuilderDeployment(ctx context.Context, env *fv1.Environment) {
	ns := envw.nsResolver.GetBuilderNS(env.ObjectMeta.Namespace)
	deployList, err := envw.getBuilderDeploymentList(ctx, envw.getDeploymentLabels(env.ObjectMeta.Name), ns)
	if err != nil {
		envw.logger.Error("error getting the builder deployment list", zap.Error(err))
	}
	for _, deploy := range deployList {
		if _, ok := envw.cache[crd.CacheKeyUIDFromMeta(&env.ObjectMeta)]; ok {
			err := envw.deleteBuilderDeploymentByName(ctx, deploy.ObjectMeta.Name, deploy.ObjectMeta.Namespace)
			if err != nil {
				envw.logger.Error("error removing builder deployment", zap.Error(err),
					zap.String("deployment_name", deploy.ObjectMeta.Name),
					zap.String("deployment_namespace", deploy.ObjectMeta.Namespace))
			}
			break
		} else {
			envw.logger.Error("builder deployment not found", zap.Error(err),
				zap.String("deployment_name", deploy.ObjectMeta.Name),
				zap.String("deployment_namespace", deploy.ObjectMeta.Namespace))
		}
	}
}

func (envw *environmentWatcher) createBuilder(ctx context.Context, env *fv1.Environment, ns string) (*builderInfo, error) {
	var svc *apiv1.Service
	var deploy *appsv1.Deployment

	sel := envw.getLabels(env.ObjectMeta.Name, ns, env.ObjectMeta.ResourceVersion)

	svcList, err := envw.getBuilderServiceList(ctx, sel, ns)
	if err != nil {
		return nil, err
	}
	// there should be only one service in svcList
	if len(svcList) == 0 {
		svc, err = envw.createBuilderService(ctx, env, ns)
		if err != nil {
			return nil, fmt.Errorf("error creating builder service for environment in namespace %s %s: %w", env.ObjectMeta.Name, ns, err)
		}
	} else if len(svcList) == 1 {
		svc = &svcList[0]
	} else {
		return nil, fmt.Errorf("found more than one builder service for environment in namespace %s %s", env.ObjectMeta.Name, ns)
	}

	deployList, err := envw.getBuilderDeploymentList(ctx, sel, ns)
	if err != nil {
		return nil, err
	}
	// there should be only one deploy in deployList
	if len(deployList) == 0 {
		deploy, err = envw.createBuilderDeployment(ctx, env, ns)
		if err != nil {
			return nil, fmt.Errorf("error creating builder deployment for environment in namespace %s %s: %w", env.ObjectMeta.Name, ns, err)
		}
	} else if len(deployList) == 1 {
		deploy = &deployList[0]
	} else {
		return nil, fmt.Errorf("found more than one builder deployment for environment in namespace %s %s", env.ObjectMeta.Name, ns)
	}

	return &builderInfo{
		envMetadata: &env.ObjectMeta,
		service:     svc,
		deployment:  deploy,
	}, nil
}

func (envw *environmentWatcher) deleteBuilderServiceByName(ctx context.Context, name, namespace string) error {
	err := envw.kubernetesClient.CoreV1().
		Services(namespace).
		Delete(ctx, name, delOpt)
	if err != nil {
		return fmt.Errorf("error deleting builder service %s.%s: %w", name, namespace, err)
	}
	return nil
}

func (envw *environmentWatcher) deleteBuilderDeploymentByName(ctx context.Context, name, namespace string) error {
	err := envw.kubernetesClient.AppsV1().
		Deployments(namespace).
		Delete(ctx, name, delOpt)
	if err != nil {
		return fmt.Errorf("error deleting builder deployment %s.%s: %w", name, namespace, err)
	}
	return nil
}

func (envw *environmentWatcher) getBuilderServiceList(ctx context.Context, sel map[string]string, ns string) ([]apiv1.Service, error) {
	svcList, err := envw.kubernetesClient.CoreV1().Services(ns).List(
		ctx,
		metav1.ListOptions{
			LabelSelector: labels.Set(sel).AsSelector().String(),
		})
	if err != nil {
		return nil, fmt.Errorf("error getting builder service list for namespace %s: %w", ns, err)
	}
	return svcList.Items, nil
}

func (envw *environmentWatcher) createBuilderService(ctx context.Context, env *fv1.Environment, ns string) (*apiv1.Service, error) {
	name := fmt.Sprintf("%v-%v", env.ObjectMeta.Name, env.ObjectMeta.ResourceVersion)
	sel := envw.getLabels(env.ObjectMeta.Name, ns, env.ObjectMeta.ResourceVersion)
	service := apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
			Labels:    sel,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(env, schema.GroupVersionKind{
					Group:   "fission.io",
					Version: "v1",
					Kind:    "Environment",
				}),
			},
		},
		Spec: apiv1.ServiceSpec{
			Selector: sel,
			Type:     apiv1.ServiceTypeClusterIP,
			Ports: []apiv1.ServicePort{
				{
					Name:     "fetcher-port",
					Protocol: apiv1.ProtocolTCP,
					Port:     8000,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 8000,
					},
				},
				{
					Name:     "builder-port",
					Protocol: apiv1.ProtocolTCP,
					Port:     8001,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 8001,
					},
				},
			},
		},
	}
	envw.logger.Info("creating builder service", zap.String("service_name", name))
	_, err := envw.kubernetesClient.CoreV1().Services(ns).Create(ctx, &service, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return &service, nil
}

func (envw *environmentWatcher) getBuilderDeploymentList(ctx context.Context, sel map[string]string, ns string) ([]appsv1.Deployment, error) {
	deployList, err := envw.kubernetesClient.AppsV1().Deployments(ns).List(
		ctx,
		metav1.ListOptions{
			LabelSelector: labels.Set(sel).AsSelector().String(),
		})
	if err != nil {
		return nil, fmt.Errorf("error getting builder deployment list for namespace %s: %w", ns, err)
	}
	return deployList.Items, nil
}

func (envw *environmentWatcher) createBuilderDeployment(ctx context.Context, env *fv1.Environment, ns string) (*appsv1.Deployment, error) {
	name := fmt.Sprintf("%v-%v", env.ObjectMeta.Name, env.ObjectMeta.ResourceVersion)
	sel := envw.getLabels(env.ObjectMeta.Name, ns, env.ObjectMeta.ResourceVersion)
	var replicas int32 = 1

	podAnnotations := env.ObjectMeta.Annotations
	if podAnnotations == nil {
		podAnnotations = make(map[string]string)
	}
	if envw.useIstio && env.Spec.AllowAccessToExternalNetwork {
		podAnnotations["sidecar.istio.io/inject"] = "false"
	}

	container, err := util.MergeContainer(&apiv1.Container{
		Name:                   "builder",
		Image:                  env.Spec.Builder.Image,
		ImagePullPolicy:        envw.builderImagePullPolicy,
		TerminationMessagePath: "/dev/termination-log",
		Command:                []string{"/builder", envw.fetcherConfig.SharedMountPath()},
		ReadinessProbe: &apiv1.Probe{
			InitialDelaySeconds: 5,
			PeriodSeconds:       2,
			ProbeHandler: apiv1.ProbeHandler{
				HTTPGet: &apiv1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 8001,
					},
				},
			},
		},
	}, env.Spec.Builder.Container)
	if err != nil {
		return nil, err
	}

	pod := apiv1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      sel,
			Annotations: podAnnotations,
		},
		Spec: apiv1.PodSpec{
			Containers:         []apiv1.Container{*container},
			ServiceAccountName: fv1.FissionBuilderSA,
		},
	}

	if envw.podSpecPatch != nil {

		updatedPodSpec, err := util.MergePodSpec(&pod.Spec, envw.podSpecPatch)
		if err == nil {
			pod.Spec = *updatedPodSpec
		} else {
			envw.logger.Warn("Failed to merge the specs: %v", zap.Error(err))
		}
	}

	pod.Spec = *(util.ApplyImagePullSecret(env.Spec.ImagePullSecret, pod.Spec))

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
			Labels:    sel,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(env, schema.GroupVersionKind{
					Group:   "fission.io",
					Version: "v1",
					Kind:    "Environment",
				}),
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: sel,
			},
			Template: pod,
		},
	}

	err = envw.fetcherConfig.AddFetcherToPodSpec(&deployment.Spec.Template.Spec, "builder")
	if err != nil {
		return nil, err
	}

	if env.Spec.Builder.PodSpec != nil {
		newPodSpec, err := util.MergePodSpec(&deployment.Spec.Template.Spec, env.Spec.Builder.PodSpec)
		if err != nil {
			return nil, err
		}
		deployment.Spec.Template.Spec = *newPodSpec
	}

	_, err = envw.kubernetesClient.AppsV1().Deployments(ns).Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	envw.logger.Info("creating builder deployment", zap.String("deployment", name))

	return deployment, nil
}
