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
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	multierror "github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	k8sErrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/executor/reaper"
	"github.com/fission/fission/pkg/throttler"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/maps"
)

var _ executortype.ExecutorType = &Container{}

type (
	// Container represents an executor type
	Container struct {
		logger *zap.Logger

		kubernetesClient *kubernetes.Clientset
		fissionClient    *crd.FissionClient
		instanceID       string
		// fetcherConfig    *fetcherConfig.Config

		runtimeImagePullPolicy apiv1.PullPolicy
		namespace              string
		useIstio               bool

		fsCache *fscache.FunctionServiceCache // cache funcSvc's by function, address and pod name

		throttler *throttler.Throttler

		funcInformer       *k8sCache.SharedIndexInformer
		serviceInformer    k8sCache.SharedIndexInformer
		deploymentInformer k8sCache.SharedIndexInformer

		defaultIdlePodReapTime time.Duration
	}
)

// MakeContainer initializes and returns an instance of CaaF
func MakeContainer(
	logger *zap.Logger,
	fissionClient *crd.FissionClient,
	kubernetesClient *kubernetes.Clientset,
	namespace string,
	instanceID string,
	funcInformer *k8sCache.SharedIndexInformer) (executortype.ExecutorType, error) {
	enableIstio := false
	if len(os.Getenv("ENABLE_ISTIO")) > 0 {
		istio, err := strconv.ParseBool(os.Getenv("ENABLE_ISTIO"))
		if err != nil {
			logger.Error("failed to parse 'ENABLE_ISTIO', set to false", zap.Error(err))
		}
		enableIstio = istio
	}

	caaf := &Container{
		logger: logger.Named("CaaF"),

		fissionClient:    fissionClient,
		kubernetesClient: kubernetesClient,
		instanceID:       instanceID,

		namespace: namespace,
		fsCache:   fscache.MakeFunctionServiceCache(logger),
		throttler: throttler.MakeThrottler(1 * time.Minute),

		funcInformer: funcInformer,

		runtimeImagePullPolicy: utils.GetImagePullPolicy(os.Getenv("RUNTIME_IMAGE_PULL_POLICY")),
		useIstio:               enableIstio,
		// Time is set slightly higher than NewDeploy as cold starts are longer for CaaF
		defaultIdlePodReapTime: 1 * time.Minute,
	}

	(*caaf.funcInformer).AddEventHandler(caaf.FuncInformerHandler())

	informerFactory, err := utils.GetInformerFactoryByExecutor(caaf.kubernetesClient, fv1.ExecutorTypeContainer)
	if err != nil {
		return nil, err
	}
	caaf.serviceInformer = informerFactory.Core().V1().Services().Informer()
	caaf.deploymentInformer = informerFactory.Apps().V1().Deployments().Informer()

	return caaf, nil
}

// Run start the function along with an object reaper.
func (caaf *Container) Run(ctx context.Context) {
	go caaf.idleObjectReaper()
}

// GetTypeName returns the executor type name.
func (caaf *Container) GetTypeName() fv1.ExecutorType {
	return fv1.ExecutorTypeContainer
}

// GetTotalAvailable has not been implemented for CaaF.
func (caaf *Container) GetTotalAvailable(fn *fv1.Function) int {
	// Not Implemented for CaaF.
	return 0
}

// UnTapService has not been implemented for CaaF.
func (caaf *Container) UnTapService(key string, svcHost string) {
	// Not Implemented for CaaF.
}

// GetFuncSvc returns a function service; error otherwise.
func (caaf *Container) GetFuncSvc(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	return caaf.createFunction(fn)
}

// GetFuncSvcFromCache returns a function service from cache; error otherwise.
func (caaf *Container) GetFuncSvcFromCache(fn *fv1.Function) (*fscache.FuncSvc, error) {
	return caaf.fsCache.GetByFunction(&fn.ObjectMeta)
}

// DeleteFuncSvcFromCache deletes a function service from cache.
func (caaf *Container) DeleteFuncSvcFromCache(fsvc *fscache.FuncSvc) {
	caaf.fsCache.DeleteEntry(fsvc)
}

// GetFuncSvcFromPoolCache has not been implemented for Container Functions
func (caaf *Container) GetFuncSvcFromPoolCache(fn *fv1.Function, requestsPerPod int) (*fscache.FuncSvc, int, error) {
	// Not Implemented for NewDeployment. Will be used when support of concurrent specialization of same function is added.
	return nil, 0, nil
}

// TapService makes a TouchByAddress request to the cache.
func (caaf *Container) TapService(svcHost string) error {
	err := caaf.fsCache.TouchByAddress(svcHost)
	if err != nil {
		return err
	}
	return nil
}

func (caaf *Container) getServiceInfo(obj apiv1.ObjectReference) (*apiv1.Service, error) {
	item, exists, err := utils.GetCachedItem(obj, caaf.serviceInformer)

	if err != nil || !exists {
		caaf.logger.Debug(
			"Falling back to getting service info from k8s API -- this may cause performance issues for your function.",
			zap.Bool("exists", exists),
			zap.Error(err),
		)
		service, err := caaf.kubernetesClient.CoreV1().Services(obj.Namespace).Get(context.TODO(), obj.Name, metav1.GetOptions{})
		return service, err
	}

	service := item.(*apiv1.Service)
	return service, nil
}

func (caaf *Container) getDeploymentInfo(obj apiv1.ObjectReference) (*appsv1.Deployment, error) {
	item, exists, err := utils.GetCachedItem(obj, caaf.deploymentInformer)

	if err != nil || !exists {
		caaf.logger.Debug(
			"Falling back to getting deployment info from k8s API -- this may cause performance issues for your function.",
			zap.Bool("exists", exists),
			zap.Error(err),
		)
		deployment, err := caaf.kubernetesClient.AppsV1().Deployments(obj.Namespace).Get(context.TODO(), obj.Name, metav1.GetOptions{})
		return deployment, err
	}

	deployment := item.(*appsv1.Deployment)
	return deployment, nil
}

// IsValid does a get on the service address to ensure it's a valid service, then
// scale deployment to 1 replica if there are no available replicas for function.
// Return true if no error occurs, return false otherwise.
func (caaf *Container) IsValid(fsvc *fscache.FuncSvc) bool {
	if len(strings.Split(fsvc.Address, ".")) == 0 {
		caaf.logger.Error("address not found in function service")
		return false
	}
	if len(fsvc.KubernetesObjects) == 0 {
		caaf.logger.Error("no kubernetes object related to function", zap.String("function", fsvc.Function.Name))
		return false
	}
	for _, obj := range fsvc.KubernetesObjects {
		if strings.ToLower(obj.Kind) == "service" {
			_, err := caaf.getServiceInfo(obj)
			if err != nil {
				if !k8sErrs.IsNotFound(err) {
					caaf.logger.Error("error validating function service", zap.String("function", fsvc.Function.Name), zap.Error(err))
				}
				return false
			}

		} else if strings.ToLower(obj.Kind) == "deployment" {
			currentDeploy, err := caaf.getDeploymentInfo(obj)
			if err != nil {
				if !k8sErrs.IsNotFound(err) {
					caaf.logger.Error("error validating function deployment", zap.String("function", fsvc.Function.Name), zap.Error(err))
				}
				return false
			}
			if currentDeploy.Status.AvailableReplicas < 1 {
				return false
			}
		}
	}
	return true
}

// RefreshFuncPods deletes pods related to the function so that new pods are replenished
func (caaf *Container) RefreshFuncPods(logger *zap.Logger, f fv1.Function) error {

	funcLabels := caaf.getDeployLabels(f.ObjectMeta)

	dep, err := caaf.kubernetesClient.AppsV1().Deployments(metav1.NamespaceAll).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labels.Set(funcLabels).AsSelector().String(),
	})
	if err != nil {
		return err
	}

	// Ideally there should be only one deployment but for now we rely on label/selector to ensure that condition
	for _, deployment := range dep.Items {
		rvCount, err := referencedResourcesRVSum(caaf.kubernetesClient, deployment.Namespace, f.Spec.Secrets, f.Spec.ConfigMaps)
		if err != nil {
			return err
		}

		patch := fmt.Sprintf(`{"spec" : {"template": {"spec":{"containers":[{"name": "%s", "env":[{"name": "%s", "value": "%v"}]}]}}}}`,
			f.ObjectMeta.Name, fv1.ResourceVersionCount, rvCount)

		_, err = caaf.kubernetesClient.AppsV1().Deployments(deployment.ObjectMeta.Namespace).Patch(context.TODO(), deployment.ObjectMeta.Name,
			k8sTypes.StrategicMergePatchType,
			[]byte(patch), metav1.PatchOptions{})
		if err != nil {
			return err
		}
	}
	return nil
}

// AdoptExistingResources attempts to adopt resources for functions in all namespaces.
func (caaf *Container) AdoptExistingResources() {
	fnList, err := caaf.fissionClient.CoreV1().Functions(metav1.NamespaceAll).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		caaf.logger.Error("error getting function list", zap.Error(err))
		return
	}

	wg := &sync.WaitGroup{}

	for i := range fnList.Items {
		fn := &fnList.Items[i]
		if fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypeContainer {
			wg.Add(1)
			go func() {
				defer wg.Done()

				_, err = caaf.fnCreate(fn)
				if err != nil {
					caaf.logger.Warn("failed to adopt resources for function", zap.Error(err))
					return
				}
				caaf.logger.Info("adopt resources for function", zap.String("function", fn.ObjectMeta.Name))
			}()
		}
	}

	wg.Wait()
}

// CleanupOldExecutorObjects cleans orphaned resources.
func (caaf *Container) CleanupOldExecutorObjects() {
	caaf.logger.Info("CaaF starts to clean orphaned resources", zap.String("instanceID", caaf.instanceID))

	errs := &multierror.Error{}
	listOpts := metav1.ListOptions{
		LabelSelector: labels.Set(map[string]string{fv1.EXECUTOR_TYPE: string(fv1.ExecutorTypeContainer)}).AsSelector().String(),
	}

	err := reaper.CleanupHpa(caaf.logger, caaf.kubernetesClient, caaf.instanceID, listOpts)
	if err != nil {
		errs = multierror.Append(errs, err)
	}

	err = reaper.CleanupDeployments(caaf.logger, caaf.kubernetesClient, caaf.instanceID, listOpts)
	if err != nil {
		errs = multierror.Append(errs, err)
	}

	err = reaper.CleanupServices(caaf.logger, caaf.kubernetesClient, caaf.instanceID, listOpts)
	if err != nil {
		errs = multierror.Append(errs, err)
	}

	if errs.ErrorOrNil() != nil {
		// TODO retry reaper; logged and ignored for now
		caaf.logger.Error("Failed to cleanup old executor objects", zap.Error(err))
	}
}

func (caaf *Container) createFunction(fn *fv1.Function) (*fscache.FuncSvc, error) {
	if fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeContainer {
		return nil, nil
	}

	fsvcObj, err := caaf.throttler.RunOnce(string(fn.ObjectMeta.UID), func(ableToCreate bool) (interface{}, error) {
		if ableToCreate {
			return caaf.fnCreate(fn)
		}
		return caaf.fsCache.GetByFunctionUID(fn.ObjectMeta.UID)
	})
	if err != nil {
		e := "error creating k8s resources for function"
		caaf.logger.Error(e,
			zap.Error(err),
			zap.String("function_name", fn.ObjectMeta.Name),
			zap.String("function_namespace", fn.ObjectMeta.Namespace))
		return nil, errors.Wrapf(err, "%s %s_%s", e, fn.ObjectMeta.Name, fn.ObjectMeta.Namespace)
	}

	fsvc, ok := fsvcObj.(*fscache.FuncSvc)
	if !ok {
		caaf.logger.Panic("receive unknown object while creating function - expected pointer of function service object")
	}

	return fsvc, err
}

func (caaf *Container) deleteFunction(fn *fv1.Function) error {
	if fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeContainer {
		return nil
	}
	err := caaf.fnDelete(fn)
	if err != nil {
		err = errors.Wrapf(err, "error deleting kubernetes objects of function %v", fn.ObjectMeta)
	}
	return err
}

func (caaf *Container) fnCreate(fn *fv1.Function) (*fscache.FuncSvc, error) {
	cleanupFunc := func(ns string, name string) {
		err := caaf.cleanupContainer(ns, name)
		if err != nil {
			caaf.logger.Error("received error while cleaning function resources",
				zap.String("namespace", ns), zap.String("name", name))
		}
	}
	objName := caaf.getObjName(fn)
	deployLabels := caaf.getDeployLabels(fn.ObjectMeta)
	deployAnnotations := caaf.getDeployAnnotations(fn.ObjectMeta)

	// to support backward compatibility, if the function was created in default ns, we fall back to creating the
	// deployment of the function in fission-function ns
	ns := caaf.namespace
	if fn.ObjectMeta.Namespace != metav1.NamespaceDefault {
		ns = fn.ObjectMeta.Namespace
	}

	// Envoy(istio-proxy) returns 404 directly before istio pilot
	// propagates latest Envoy-specific configuration.
	// Since Container waits for pods of deployment to be ready,
	// change the order of kubeObject creation (create service first,
	// then deployment) to take advantage of waiting time.
	svc, err := caaf.createOrGetSvc(fn, deployLabels, deployAnnotations, objName, ns)
	if err != nil {
		caaf.logger.Error("error creating service", zap.Error(err), zap.String("service", objName))
		go cleanupFunc(ns, objName)
		return nil, errors.Wrapf(err, "error creating service %v", objName)
	}
	svcAddress := fmt.Sprintf("%v.%v", svc.Name, svc.Namespace)

	depl, err := caaf.createOrGetDeployment(fn, objName, deployLabels, deployAnnotations, ns)
	if err != nil {
		caaf.logger.Error("error creating deployment", zap.Error(err), zap.String("deployment", objName))
		go cleanupFunc(ns, objName)
		return nil, errors.Wrapf(err, "error creating deployment %v", objName)
	}

	hpa, err := caaf.createOrGetHpa(objName, &fn.Spec.InvokeStrategy.ExecutionStrategy, depl, deployLabels, deployAnnotations)
	if err != nil {
		caaf.logger.Error("error creating HPA", zap.Error(err), zap.String("hpa", objName))
		go cleanupFunc(ns, objName)
		return nil, errors.Wrapf(err, "error creating the HPA %v", objName)
	}

	kubeObjRefs := []apiv1.ObjectReference{
		{
			//obj.TypeMeta.Kind does not work hence this, needs investigation and a fix
			Kind:            "deployment",
			Name:            depl.ObjectMeta.Name,
			APIVersion:      depl.TypeMeta.APIVersion,
			Namespace:       depl.ObjectMeta.Namespace,
			ResourceVersion: depl.ObjectMeta.ResourceVersion,
			UID:             depl.ObjectMeta.UID,
		},
		{
			Kind:            "service",
			Name:            svc.ObjectMeta.Name,
			APIVersion:      svc.TypeMeta.APIVersion,
			Namespace:       svc.ObjectMeta.Namespace,
			ResourceVersion: svc.ObjectMeta.ResourceVersion,
			UID:             svc.ObjectMeta.UID,
		},
		{
			Kind:            "horizontalpodautoscaler",
			Name:            hpa.ObjectMeta.Name,
			APIVersion:      hpa.TypeMeta.APIVersion,
			Namespace:       hpa.ObjectMeta.Namespace,
			ResourceVersion: hpa.ObjectMeta.ResourceVersion,
			UID:             hpa.ObjectMeta.UID,
		},
	}

	fsvc := &fscache.FuncSvc{
		Name:              objName,
		Function:          &fn.ObjectMeta,
		Address:           svcAddress,
		KubernetesObjects: kubeObjRefs,
		Executor:          fv1.ExecutorTypeContainer,
	}

	_, err = caaf.fsCache.Add(*fsvc)
	if err != nil {
		caaf.logger.Error("error adding function to cache", zap.Error(err), zap.Any("function", fsvc.Function))
		return fsvc, err
	}

	caaf.fsCache.IncreaseColdStarts(fn.ObjectMeta.Name, string(fn.ObjectMeta.UID))

	return fsvc, nil
}

func (caaf *Container) updateFunction(oldFn *fv1.Function, newFn *fv1.Function) error {

	if oldFn.ObjectMeta.ResourceVersion == newFn.ObjectMeta.ResourceVersion {
		return nil
	}

	// Ignoring updates to functions which are not of Container type
	if newFn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeContainer &&
		oldFn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeContainer {
		return nil
	}

	// Executor type is no longer Container
	if newFn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeContainer &&
		oldFn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypeContainer {
		caaf.logger.Info("function does not use new deployment executor anymore, deleting resources",
			zap.Any("function", newFn))
		// IMP - pass the oldFn, as the new/modified function is not in cache
		return caaf.deleteFunction(oldFn)
	}

	// Executor type changed to Container from something else
	if oldFn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeContainer &&
		newFn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypeContainer {
		caaf.logger.Info("function type changed to Container, creating resources",
			zap.Any("old_function", oldFn.ObjectMeta),
			zap.Any("new_function", newFn.ObjectMeta))
		_, err := caaf.createFunction(newFn)
		if err != nil {
			caaf.updateStatus(oldFn, err, "error changing the function's type to Container")
		}
		return err
	}

	if oldFn.Spec.InvokeStrategy != newFn.Spec.InvokeStrategy {

		// to support backward compatibility, if the function was created in default ns, we fall back to creating the
		// deployment of the function in fission-function ns, so cleaning up resources there
		ns := caaf.namespace
		if newFn.ObjectMeta.Namespace != metav1.NamespaceDefault {
			ns = newFn.ObjectMeta.Namespace
		}

		fsvc, err := caaf.fsCache.GetByFunctionUID(newFn.ObjectMeta.UID)
		if err != nil {
			err = errors.Wrapf(err, "error updating function due to unable to find function service cache: %v", oldFn)
			return err
		}

		hpa, err := caaf.getHpa(ns, fsvc.Name)
		if err != nil {
			caaf.updateStatus(oldFn, err, "error getting HPA while updating function")
			return err
		}

		hpaChanged := false

		if newFn.Spec.InvokeStrategy.ExecutionStrategy.MinScale != oldFn.Spec.InvokeStrategy.ExecutionStrategy.MinScale {
			replicas := int32(newFn.Spec.InvokeStrategy.ExecutionStrategy.MinScale)
			hpa.Spec.MinReplicas = &replicas
			hpaChanged = true
		}

		if newFn.Spec.InvokeStrategy.ExecutionStrategy.MaxScale != oldFn.Spec.InvokeStrategy.ExecutionStrategy.MaxScale {
			hpa.Spec.MaxReplicas = int32(newFn.Spec.InvokeStrategy.ExecutionStrategy.MaxScale)
			hpaChanged = true
		}

		if newFn.Spec.InvokeStrategy.ExecutionStrategy.TargetCPUPercent != oldFn.Spec.InvokeStrategy.ExecutionStrategy.TargetCPUPercent {
			targetCpupercent := int32(newFn.Spec.InvokeStrategy.ExecutionStrategy.TargetCPUPercent)
			hpa.Spec.TargetCPUUtilizationPercentage = &targetCpupercent
			hpaChanged = true
		}

		if hpaChanged {
			err := caaf.updateHpa(hpa)
			if err != nil {
				caaf.updateStatus(oldFn, err, "error updating HPA while updating function")
				return err
			}
		}
	}

	deployChanged := false

	// If length of slice has changed then no need to check individual elements
	if len(oldFn.Spec.Secrets) != len(newFn.Spec.Secrets) {
		deployChanged = true
	} else {
		for i, newSecret := range newFn.Spec.Secrets {
			if newSecret != oldFn.Spec.Secrets[i] {
				deployChanged = true
				break
			}
		}
	}
	if len(oldFn.Spec.ConfigMaps) != len(newFn.Spec.ConfigMaps) {
		deployChanged = true
	} else {
		for i, newConfig := range newFn.Spec.ConfigMaps {
			if newConfig != oldFn.Spec.ConfigMaps[i] {
				deployChanged = true
				break
			}
		}
	}

	if !reflect.DeepEqual(oldFn.Spec.PodSpec, newFn.Spec.PodSpec) {
		deployChanged = true
	}

	if deployChanged {
		return caaf.updateFuncDeployment(newFn)
	}

	return nil
}

func (caaf *Container) updateFuncDeployment(fn *fv1.Function) error {

	fsvc, err := caaf.fsCache.GetByFunctionUID(fn.ObjectMeta.UID)
	if err != nil {
		err = errors.Wrapf(err, "error updating function due to unable to find function service cache: %v", fn)
		return err
	}
	fnObjName := fsvc.Name

	deployLabels := caaf.getDeployLabels(fn.ObjectMeta)
	caaf.logger.Info("updating deployment due to function update",
		zap.String("deployment", fnObjName), zap.Any("function", fn.ObjectMeta.Name))

	// to support backward compatibility, if the function was created in default ns, we fall back to creating the
	// deployment of the function in fission-function ns
	ns := caaf.namespace
	if fn.ObjectMeta.Namespace != metav1.NamespaceDefault {
		ns = fn.ObjectMeta.Namespace
	}

	existingDepl, err := caaf.kubernetesClient.AppsV1().Deployments(ns).Get(context.TODO(), fnObjName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// the resource version inside function packageRef is changed,
	// so the content of fetchRequest in deployment cmd is different.
	// Therefore, the deployment update will trigger a rolling update.
	newDeployment, err := caaf.getDeploymentSpec(fn, existingDepl.Spec.Replicas, // use current replicas instead of minscale in the ExecutionStrategy.
		fnObjName, ns, deployLabels, caaf.getDeployAnnotations(fn.ObjectMeta))
	if err != nil {
		caaf.updateStatus(fn, err, "failed to get new deployment spec while updating function")
		return err
	}

	err = caaf.updateDeployment(newDeployment, ns)
	if err != nil {
		caaf.updateStatus(fn, err, "failed to update deployment while updating function")
		return err
	}

	return nil
}

func (caaf *Container) fnDelete(fn *fv1.Function) error {
	multierr := &multierror.Error{}

	// GetByFunction uses resource version as part of cache key, however,
	// the resource version in function metadata will be changed when a function
	// is deleted and cause Container backend fails to delete the entry.
	// Use GetByFunctionUID instead of GetByFunction here to find correct
	// fsvc entry.
	fsvc, err := caaf.fsCache.GetByFunctionUID(fn.ObjectMeta.UID)
	if err != nil {
		err = errors.Wrap(err, fmt.Sprintf("fsvc not found in cache: %v", fn.ObjectMeta))
		return err
	}

	objName := fsvc.Name

	_, err = caaf.fsCache.DeleteOld(fsvc, time.Second*0)
	if err != nil {
		multierr = multierror.Append(multierr,
			errors.Wrapf(err, "error deleting the function from cache"))
	}

	// to support backward compatibility, if the function was created in default ns, we fall back to creating the
	// deployment of the function in fission-function ns, so cleaning up resources there
	ns := caaf.namespace
	if fn.ObjectMeta.Namespace != metav1.NamespaceDefault {
		ns = fn.ObjectMeta.Namespace
	}

	err = caaf.cleanupContainer(ns, objName)
	multierr = multierror.Append(multierr, err)

	return multierr.ErrorOrNil()
}

// getObjName returns a unique name for kubernetes objects of function
func (caaf *Container) getObjName(fn *fv1.Function) string {
	// use meta uuid of function, this ensure we always get the same name for the same function.
	uid := fn.ObjectMeta.UID[len(fn.ObjectMeta.UID)-17:]
	var functionMetadata string
	if len(fn.ObjectMeta.Name)+len(fn.ObjectMeta.Namespace) < 35 {
		functionMetadata = fn.ObjectMeta.Name + "-" + fn.ObjectMeta.Namespace
	} else {
		if len(fn.ObjectMeta.Name) > 17 {
			functionMetadata = fn.ObjectMeta.Name[:17]
		} else {
			functionMetadata = fn.ObjectMeta.Name
		}
		if len(fn.ObjectMeta.Namespace) > 17 {
			functionMetadata = functionMetadata + "-" + fn.ObjectMeta.Namespace[:17]
		} else {
			functionMetadata = functionMetadata + "-" + fn.ObjectMeta.Namespace
		}
	}
	// contructed name should be 63 characters long, as it is a valid k8s name
	// functionMetadata should be 35 characters long, as we take 17 characters from functionUid
	// with newdeploy 10 character prefix
	return strings.ToLower(fmt.Sprintf("container-%s-%s", functionMetadata, uid))
}

func (caaf *Container) getDeployLabels(fnMeta metav1.ObjectMeta) map[string]string {
	deployLabels := maps.CopyStringMap(fnMeta.Labels)
	deployLabels[fv1.EXECUTOR_TYPE] = string(fv1.ExecutorTypeContainer)
	deployLabels[fv1.FUNCTION_NAME] = fnMeta.Name
	deployLabels[fv1.FUNCTION_NAMESPACE] = fnMeta.Namespace
	deployLabels[fv1.FUNCTION_UID] = string(fnMeta.UID)
	return deployLabels
}

func (caaf *Container) getDeployAnnotations(fnMeta metav1.ObjectMeta) map[string]string {
	deployAnnotations := maps.CopyStringMap(fnMeta.Annotations)
	deployAnnotations[fv1.EXECUTOR_INSTANCEID_LABEL] = caaf.instanceID
	deployAnnotations[fv1.FUNCTION_RESOURCE_VERSION] = fnMeta.ResourceVersion
	return deployAnnotations
}

// updateStatus is a function which updates status of update.
// Current implementation only logs messages, in future it will update function status
func (caaf *Container) updateStatus(fn *fv1.Function, err error, message string) {
	caaf.logger.Error("function status update", zap.Error(err), zap.Any("function", fn), zap.String("message", message))
}

// idleObjectReaper reaps objects after certain idle time
func (caaf *Container) idleObjectReaper() {

	pollSleep := 5 * time.Second
	for {
		time.Sleep(pollSleep)

		funcSvcs, err := caaf.fsCache.ListOld(pollSleep)
		if err != nil {
			caaf.logger.Error("error reaping idle pods", zap.Error(err))
			continue
		}

		for i := range funcSvcs {
			fsvc := funcSvcs[i]

			if fsvc.Executor != fv1.ExecutorTypeContainer {
				continue
			}

			fn, err := caaf.fissionClient.CoreV1().Functions(fsvc.Function.Namespace).Get(context.TODO(), fsvc.Function.Name, metav1.GetOptions{})
			if err != nil {
				// CaaF manager handles the function delete event and clean cache/kubeobjs itself,
				// so we ignore the not found error for functions with CaaF executor type here.
				if k8sErrs.IsNotFound(err) && fsvc.Executor == fv1.ExecutorTypeContainer {
					continue
				}
				caaf.logger.Error("error getting function", zap.Error(err), zap.String("function", fsvc.Function.Name))
				continue
			}

			idlePodReapTime := caaf.defaultIdlePodReapTime
			if fn.Spec.IdleTimeout != nil {
				idlePodReapTime = time.Duration(*fn.Spec.IdleTimeout) * time.Second
			}

			if time.Since(fsvc.Atime) < idlePodReapTime {
				continue
			}

			go func() {
				deployObj := getDeploymentObj(fsvc.KubernetesObjects)
				if deployObj == nil {
					caaf.logger.Error("error finding function deployment", zap.Error(err), zap.String("function", fsvc.Function.Name))
					return
				}

				currentDeploy, err := caaf.kubernetesClient.AppsV1().
					Deployments(deployObj.Namespace).Get(context.TODO(), deployObj.Name, metav1.GetOptions{})
				if err != nil {
					caaf.logger.Error("error getting function deployment", zap.Error(err), zap.String("function", fsvc.Function.Name))
					return
				}

				minScale := int32(fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale)

				// do nothing if the current replicas is already lower than minScale
				if *currentDeploy.Spec.Replicas <= minScale {
					return
				}

				err = caaf.scaleDeployment(deployObj.Namespace, deployObj.Name, minScale)
				if err != nil {
					caaf.logger.Error("error scaling down function deployment", zap.Error(err), zap.String("function", fsvc.Function.Name))
				}
			}()
		}
	}
}

func getDeploymentObj(kubeobjs []apiv1.ObjectReference) *apiv1.ObjectReference {
	for _, kubeobj := range kubeobjs {
		switch strings.ToLower(kubeobj.Kind) {
		case "deployment":
			return &kubeobj
		}
	}
	return nil
}
