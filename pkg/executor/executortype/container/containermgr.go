// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	k8sErrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/executor/executortype"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/executor/metrics"
	"github.com/fission/fission/pkg/executor/reaper"
	"github.com/fission/fission/pkg/executor/reaper/idle"
	executorUtils "github.com/fission/fission/pkg/executor/util"
	hpautils "github.com/fission/fission/pkg/executor/util/hpa"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/throttler"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/maps"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

var (
	_ executortype.ExecutorType = &Container{}
)

type (
	// Container represents an executor type
	Container struct {
		logger logr.Logger

		kubernetesClient kubernetes.Interface
		fissionClient    versioned.Interface
		instanceID       string
		nsResolver       *utils.NamespaceResolver

		runtimeImagePullPolicy apiv1.PullPolicy
		useIstio               bool

		fsCache *fscache.FunctionServiceCache // cache funcSvc's by function, address and pod name

		throttler *throttler.Throttler

		defaultIdlePodReapTime time.Duration

		// crClient is the executor Manager's cache-backed client, used by IsValid
		// to read function Deployments/Services from the shared Manager cache
		// (replacing this type's standalone SharedInformerFactory listers). Set in
		// RegisterReconcilers once the Manager exists.
		crClient client.Client

		hpaops                     *hpautils.HpaOperations
		objectReaperIntervalSecond time.Duration

		enableOwnerReferences bool
	}
)

// MakeContainer initializes and returns an instance of CaaF
func MakeContainer(
	ctx context.Context,
	logger logr.Logger,
	fissionClient versioned.Interface,
	kubernetesClient kubernetes.Interface,
	instanceID string,
) (executortype.ExecutorType, error) {
	enableIstio := false
	if len(os.Getenv("ENABLE_ISTIO")) > 0 {
		istio, err := strconv.ParseBool(os.Getenv("ENABLE_ISTIO"))
		if err != nil {
			logger.Error(err, "failed to parse 'ENABLE_ISTIO', set to false")
		}
		enableIstio = istio
	}

	caaf := &Container{
		logger: logger.WithName("CaaF"),

		fissionClient:    fissionClient,
		kubernetesClient: kubernetesClient,
		instanceID:       instanceID,
		nsResolver:       utils.DefaultNSResolver(),

		fsCache:   fscache.MakeFunctionServiceCache(logger),
		throttler: throttler.MakeThrottler(1 * time.Minute),

		runtimeImagePullPolicy: utils.GetImagePullPolicy(os.Getenv("RUNTIME_IMAGE_PULL_POLICY")),
		useIstio:               enableIstio,
		// Time is set slightly higher than NewDeploy as cold starts are longer for CaaF
		defaultIdlePodReapTime:     1 * time.Minute,
		objectReaperIntervalSecond: time.Duration(executorUtils.GetObjectReaperInterval(logger, fv1.ExecutorTypeContainer, 5)) * time.Second,
		hpaops:                     hpautils.NewHpaOperations(logger, kubernetesClient, instanceID),

		enableOwnerReferences: utils.IsOwnerReferencesEnabled(),
	}

	// The Function watch is a controller-runtime reconciler registered via the
	// shared funcreconciler on the executor Manager (see reconciler.go).
	// Deployment/Service reads (IsValid) go through the Manager's cache-backed
	// client (caaf.crClient), set in RegisterReconcilers — no per-type informer
	// factory is needed.
	return caaf, nil
}

// Run is a no-op: the container manager no longer runs its own informer factory.
// Its Deployment/Service reads (IsValid) go through the executor Manager's cache,
// which controller-runtime syncs before any runnable (including this type's
// reapers) starts.
func (caaf *Container) Run(context.Context, *errgroup.Group) {}

// GetTypeName returns the executor type name.
func (caaf *Container) GetTypeName(ctx context.Context) fv1.ExecutorType {
	return fv1.ExecutorTypeContainer
}

// UnTapService has not been implemented for CaaF.
func (caaf *Container) UnTapService(ctx context.Context, fnMeta *metav1.ObjectMeta, svcHost string) {
	// Not Implemented for CaaF.
}

// MarkSpecializationFailure has not been implemented for CaaF.
func (caaf *Container) MarkSpecializationFailure(ctx context.Context, fnMeta *metav1.ObjectMeta) {
	// Not Implemented for CaaF.
}

// GetFuncSvc returns a function service; error otherwise.
func (caaf *Container) GetFuncSvc(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	return caaf.createFunction(ctx, fn)
}

// GetFuncSvcFromCache returns a function service from cache; error otherwise.
func (caaf *Container) GetFuncSvcFromCache(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	otelUtils.SpanTrackEvent(ctx, "GetFuncSvcFromCache", otelUtils.GetAttributesForFunction(fn)...)
	return caaf.fsCache.GetByFunctionUID(fn.UID)
}

// DeleteFuncSvcFromCache deletes a function service from cache.
func (caaf *Container) DeleteFuncSvcFromCache(ctx context.Context, fsvc *fscache.FuncSvc) {
	caaf.fsCache.DeleteEntry(fsvc)
}

// TapService makes a TouchByAddress request to the cache.
func (caaf *Container) TapService(ctx context.Context, svcHost string) error {
	err := caaf.fsCache.TouchByAddress(svcHost)
	if err != nil {
		return err
	}
	return nil
}

// IsValid does a get on the service address to ensure it's a valid service, then
// scale deployment to 1 replica if there are no available replicas for function.
// Return true if no error occurs, return false otherwise.
func (caaf *Container) IsValid(ctx context.Context, fsvc *fscache.FuncSvc) bool {
	logger := otelUtils.LoggerWithTraceID(ctx, caaf.logger)
	otelUtils.SpanTrackEvent(ctx, "IsValid", fscache.GetAttributesForFuncSvc(fsvc)...)
	if len(strings.Split(fsvc.Address, ".")) == 0 {
		logger.Info("address not found in function service")
		return false
	}
	if len(fsvc.KubernetesObjects) == 0 {
		logger.Info("no kubernetes object related to function", "function", fsvc.Function.Name)
		return false
	}
	for _, obj := range fsvc.KubernetesObjects {
		objKey := client.ObjectKey{Namespace: obj.Namespace, Name: obj.Name}
		if strings.ToLower(obj.Kind) == "service" {
			if err := caaf.crClient.Get(ctx, objKey, &apiv1.Service{}); err != nil {
				if !k8sErrs.IsNotFound(err) {
					logger.Error(err, "error validating function service", "function", fsvc.Function.Name)
				}
				return false
			}
		} else if strings.ToLower(obj.Kind) == "deployment" {
			currentDeploy := &appsv1.Deployment{}
			if err := caaf.crClient.Get(ctx, objKey, currentDeploy); err != nil {
				if !k8sErrs.IsNotFound(err) {
					logger.Error(err, "error validating function deployment", "function", fsvc.Function.Name)
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
func (caaf *Container) RefreshFuncPods(ctx context.Context, logger logr.Logger, f fv1.Function) error {

	funcLabels := caaf.getDeployLabels(f.ObjectMeta)

	nsResolver := utils.DefaultNSResolver()
	dep, err := caaf.kubernetesClient.AppsV1().Deployments(nsResolver.GetFunctionNS(f.ObjectMeta.Namespace)).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(funcLabels).AsSelector().String(),
	})
	if err != nil {
		return err
	}

	// Ideally there should be only one deployment but for now we rely on label/selector to ensure that condition
	for _, deployment := range dep.Items {
		rvCount, err := executorUtils.ReferencedResourcesRVSum(ctx, caaf.kubernetesClient, deployment.Namespace, f.Spec.Secrets, f.Spec.ConfigMaps)
		if err != nil {
			return err
		}

		patch := fmt.Sprintf(`{"spec" : {"template": {"spec":{"containers":[{"name": "%s", "env":[{"name": "%s", "value": "%d"}]}]}}}}`,
			f.Name, fv1.ResourceVersionCount, rvCount)

		_, err = caaf.kubernetesClient.AppsV1().Deployments(deployment.ObjectMeta.Namespace).Patch(ctx, deployment.Name,
			k8sTypes.StrategicMergePatchType,
			[]byte(patch), metav1.PatchOptions{})
		if err != nil {
			return err
		}
	}
	return nil
}

// AdoptExistingResources re-claims the function objects this executor type left
// behind on a previous run. It routes through the throttled createFunction (not
// fnCreate directly), so the adopt pass and the Function reconciler — which also
// re-stamps existing objects on its initial sync — single-flight per function
// UID instead of racing on the in-place Update (which previously produced
// resourceVersion conflicts that tripped fnCreate's destroy-on-error path).
func (caaf *Container) AdoptExistingResources(ctx context.Context) {
	executorUtils.AdoptFunctions(ctx, caaf.logger, caaf.fissionClient, fv1.ExecutorTypeContainer,
		func(ctx context.Context, fn *fv1.Function) error {
			_, err := caaf.createFunction(ctx, fn)
			return err
		})
}

// CleanupOldExecutorObjects cleans orphaned resources.
func (caaf *Container) CleanupOldExecutorObjects(ctx context.Context) {
	reaper.CleanupExecutorObjects(ctx, caaf.logger, caaf.kubernetesClient, caaf.instanceID, fv1.ExecutorTypeContainer)
}

// resourcesExist reports whether the function's backing Deployment and Service
// are present in the Manager cache. A missing object means it drifted away
// (deleted out-of-band) and the function should be recreated. Reads go through
// the cache-backed crClient (same path as IsValid).
func (caaf *Container) resourcesExist(ctx context.Context, fn *fv1.Function) (bool, error) {
	ns := caaf.nsResolver.GetFunctionNS(fn.Namespace)
	key := client.ObjectKey{Namespace: ns, Name: caaf.getObjName(fn)}
	for _, obj := range []client.Object{&appsv1.Deployment{}, &apiv1.Service{}} {
		if err := caaf.crClient.Get(ctx, key, obj); err != nil {
			if k8sErrs.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

func (caaf *Container) createFunction(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	if fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeContainer {
		return nil, nil
	}

	fsvcObj, err := caaf.throttler.RunOnce(string(fn.UID), func(ableToCreate bool) (any, error) {
		if ableToCreate {
			return caaf.fnCreate(ctx, fn)
		}
		return caaf.fsCache.GetByFunctionUID(fn.UID)
	})
	if err != nil {
		e := "error creating k8s resources for function"
		caaf.logger.Error(err, e, "function_name", fn.Name,
			"function_namespace", fn.Namespace)
		return nil, fmt.Errorf("error creating k8s resources for function %s/%s: %w", fn.Namespace, fn.Name, err)
	}

	fsvc, ok := fsvcObj.(*fscache.FuncSvc)
	if !ok {
		caaf.logger.Error(nil, "receive unknown object while creating function - expected pointer of function service object")

		panic("receive unknown object while creating function - expected pointer of function service object")
	}

	return fsvc, err
}

func (caaf *Container) deleteFunction(ctx context.Context, fn *fv1.Function) error {
	if fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeContainer {
		return nil
	}
	err := caaf.fnDelete(ctx, fn)
	if err != nil {
		return fmt.Errorf("error deleting kubernetes objects of function %s: %w", k8sCache.MetaObjectToName(fn), err)
	}
	return err
}

// destroyOnCreateError reports whether a fnCreate failure warrants tearing down
// the function's partial resources. A Conflict or AlreadyExists means the object
// exists and was concurrently modified (e.g. the adopt pass racing a reconcile,
// or two reconciles), so deleting it would turn a transient blip into a cold
// recreate — leave it for the next reconcile to converge instead. Genuine
// creation failures (quota, invalid spec, API errors) still trigger cleanup so a
// brand-new function doesn't leak half-created objects.
func destroyOnCreateError(err error) bool {
	// An explicitly cancelled context means the executor is shutting down, lost
	// leadership, or the caller gave up — not that creation genuinely failed —
	// so leave any partially-created resources for the next leader/request to
	// adopt instead of tearing them down. A context *deadline* is different: on
	// the specialization path the context carries the per-function
	// SpecializationTimeout (see pkg/executor/executor.go), so DeadlineExceeded
	// is a genuine timeout and falls through to normal cleanup.
	if errors.Is(err, context.Canceled) {
		return false
	}
	return !k8sErrs.IsConflict(err) && !k8sErrs.IsAlreadyExists(err)
}

func (caaf *Container) fnCreate(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	// Authoritative re-read: the Function object reaching this path originates
	// from the router's request body and can be stale — its DeletionTimestamp
	// may be absent even though the Function is being deleted. Without this
	// check a create can race the delete teardown (funcreconciler) and
	// re-create the Deployment/Service/HPA *after* teardown removed them,
	// leaking objects whose owning Function CR is already gone.
	live, err := caaf.fissionClient.CoreV1().Functions(fn.Namespace).Get(ctx, fn.Name, metav1.GetOptions{})
	if err != nil {
		if k8sErrs.IsNotFound(err) {
			return nil, ferror.MakeError(ferror.ErrorNotFound,
				fmt.Sprintf("function %s is gone, not creating service", k8sCache.MetaObjectToName(fn)))
		}
		return nil, err
	}
	if live.UID != fn.UID || !live.DeletionTimestamp.IsZero() {
		return nil, ferror.MakeError(ferror.ErrorNotFound,
			fmt.Sprintf("function %s is being deleted, not creating service", k8sCache.MetaObjectToName(fn)))
	}

	cleanupFunc := func(ns string, name string) {
		err := caaf.cleanupContainer(ctx, ns, name)
		if err != nil {
			caaf.logger.Error(err, "received error while cleaning function resources",
				"namespace", ns, "name", name)
		}
	}
	objName := caaf.getObjName(fn)
	deployLabels := caaf.getDeployLabels(fn.ObjectMeta)
	deployAnnotations := caaf.getDeployAnnotations(fn.ObjectMeta)

	// to support backward compatibility, if the function was created in default ns, we fall back to creating the
	// deployment of the function in fission-function ns
	ns := caaf.nsResolver.GetFunctionNS(fn.Namespace)

	// Envoy(istio-proxy) returns 404 directly before istio pilot
	// propagates latest Envoy-specific configuration.
	// Since Container waits for pods of deployment to be ready,
	// change the order of kubeObject creation (create service first,
	// then deployment) to take advantage of waiting time.
	// Transient executor errors are not written to Function.Status; see
	// the analogous note in pkg/executor/executortype/poolmgr/gp.go.
	svc, err := caaf.createOrGetSvc(ctx, fn, deployLabels, deployAnnotations, objName, ns)
	if err != nil {
		caaf.logger.Error(err, "error creating service", "service", objName)
		if destroyOnCreateError(err) {
			go cleanupFunc(ns, objName)
		}
		return nil, fmt.Errorf("error creating service %s: %w", objName, err)
	}
	svcAddress := fmt.Sprintf("%s.%s", svc.Name, svc.Namespace)

	depl, err := caaf.createOrGetDeployment(ctx, fn, objName, deployLabels, deployAnnotations, ns)
	if err != nil {
		caaf.logger.Error(err, "error creating deployment", "deployment", objName)
		if destroyOnCreateError(err) {
			go cleanupFunc(ns, objName)
		}
		return nil, fmt.Errorf("error creating deployment %s: %w", objName, err)
	}

	// For container-type functions the user image runs as a single container
	// named after the function (see deployment.go); scope HPA metrics to it.
	hpa, err := caaf.hpaops.CreateOrGetHpa(ctx, fn, objName, &fn.Spec.InvokeStrategy.ExecutionStrategy, fn.Name, depl, deployLabels, deployAnnotations)
	if err != nil {
		caaf.logger.Error(err, "error creating HPA", "hpa", objName)
		if destroyOnCreateError(err) {
			go cleanupFunc(ns, objName)
		}
		return nil, fmt.Errorf("error creating HPA %s: %w", objName, err)
	}

	kubeObjRefs := []apiv1.ObjectReference{
		{
			// obj.TypeMeta.Kind does not work hence this, needs investigation and a fix
			Kind:            "deployment",
			Name:            depl.Name,
			APIVersion:      depl.APIVersion,
			Namespace:       depl.Namespace,
			ResourceVersion: depl.ResourceVersion,
			UID:             depl.UID,
		},
		{
			Kind:            "service",
			Name:            svc.Name,
			APIVersion:      svc.APIVersion,
			Namespace:       svc.Namespace,
			ResourceVersion: svc.ResourceVersion,
			UID:             svc.UID,
		},
		{
			Kind:            "horizontalpodautoscaler",
			Name:            hpa.Name,
			APIVersion:      hpa.APIVersion,
			Namespace:       hpa.Namespace,
			ResourceVersion: hpa.ResourceVersion,
			UID:             hpa.UID,
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
		caaf.logger.Error(nil, "error adding function to cache", "function", fsvc.Function)
		metrics.RecordColdStartError(ctx, fn.Name, fn.Namespace)
		return fsvc, err
	}

	metrics.RecordColdStart(ctx, fn.Name, fn.Namespace)
	executorUtils.SetFunctionReady(ctx, caaf.logger, caaf.fissionClient, fn, fv1.FunctionReasonReady, "container deployment is ready")

	return fsvc, nil
}

func (caaf *Container) updateFunction(ctx context.Context, oldFn *fv1.Function, newFn *fv1.Function) error {

	if oldFn.ResourceVersion == newFn.ResourceVersion {
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
			"function", newFn)
		// IMP - pass the oldFn, as the new/modified function is not in cache
		return caaf.deleteFunction(ctx, oldFn)
	}

	// Executor type changed to Container from something else
	if oldFn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeContainer &&
		newFn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypeContainer {
		caaf.logger.Info("function type changed to Container, creating resources",
			"old_function", oldFn.ObjectMeta,
			"new_function", newFn.ObjectMeta)
		_, err := caaf.createFunction(ctx, newFn)
		if err != nil {
			caaf.updateStatus(oldFn, err, "error changing the function's type to Container")
		}
		return err
	}

	if !reflect.DeepEqual(oldFn.Spec.InvokeStrategy, newFn.Spec.InvokeStrategy) {
		// to support backward compatibility, if the function was created in default ns, we fall back to creating the
		// deployment of the function in fission-function ns, so cleaning up resources there
		ns := caaf.nsResolver.GetFunctionNS(newFn.Namespace)

		fsvc, err := caaf.fsCache.GetByFunctionUID(newFn.UID)
		if err != nil {
			return fmt.Errorf("error updating function due to unable to find function service cache %s: %w", k8sCache.MetaObjectToName(oldFn), err)
		}

		hpa, err := caaf.hpaops.GetHpa(ctx, ns, fsvc.Name)
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

		if !reflect.DeepEqual(newFn.Spec.InvokeStrategy.ExecutionStrategy.Metrics, oldFn.Spec.InvokeStrategy.ExecutionStrategy.Metrics) {
			// The CLI emits pod-wide Resource metrics; normalize them to
			// ContainerResource metrics scoped to the function container, the
			// same as createFunction does via CreateOrGetHpa.
			hpa.Spec.Metrics = hpautils.RewriteResourceMetricsToContainer(
				newFn.Spec.InvokeStrategy.ExecutionStrategy.Metrics, newFn.Name, caaf.logger)
			hpaChanged = true
		}

		if !reflect.DeepEqual(newFn.Spec.InvokeStrategy.ExecutionStrategy.Behavior, oldFn.Spec.InvokeStrategy.ExecutionStrategy.Behavior) {
			hpa.Spec.Behavior = newFn.Spec.InvokeStrategy.ExecutionStrategy.Behavior
			hpaChanged = true
		}

		if hpaChanged {
			err := caaf.hpaops.UpdateHpa(ctx, hpa)
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
		return caaf.updateFuncDeployment(ctx, newFn)
	}

	return nil
}

func (caaf *Container) updateFuncDeployment(ctx context.Context, fn *fv1.Function) error {

	fsvc, err := caaf.fsCache.GetByFunctionUID(fn.UID)
	if err != nil {
		return fmt.Errorf("error updating function due to unable to find function service cache %s: %w", k8sCache.MetaObjectToName(fn), err)
	}
	fnObjName := fsvc.Name

	deployLabels := caaf.getDeployLabels(fn.ObjectMeta)
	caaf.logger.Info("updating deployment due to function update",
		"deployment", fnObjName, "function", fn.Name)

	// to support backward compatibility, if the function was created in default ns, we fall back to creating the
	// deployment of the function in fission-function ns
	ns := caaf.nsResolver.GetFunctionNS(fn.Namespace)

	existingDepl, err := caaf.kubernetesClient.AppsV1().Deployments(ns).Get(ctx, fnObjName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// the resource version inside function packageRef is changed,
	// so the content of fetchRequest in deployment cmd is different.
	// Therefore, the deployment update will trigger a rolling update.
	newDeployment, err := caaf.getDeploymentSpec(ctx, fn, existingDepl.Spec.Replicas, // use current replicas instead of minscale in the ExecutionStrategy.
		fnObjName, ns, deployLabels, caaf.getDeployAnnotations(fn.ObjectMeta))
	if err != nil {
		caaf.updateStatus(fn, err, "failed to get new deployment spec while updating function")
		return err
	}

	err = caaf.updateDeployment(ctx, newDeployment, ns)
	if err != nil {
		caaf.updateStatus(fn, err, "failed to update deployment while updating function")
		return err
	}

	return nil
}

func (caaf *Container) fnDelete(ctx context.Context, fn *fv1.Function) error {
	var multierr error

	// GetByFunction now keys on UID+Generation (see #3596), not
	// ResourceVersion, but the fn passed in on delete can still carry a
	// Generation the cache entry was never keyed under (e.g. a delete
	// racing a spec update, or a stale informer snapshot) — GetByFunctionUID
	// is the UID-only lookup that doesn't depend on the caller's metadata
	// matching the cached entry's, so it's used here to find the correct
	// fsvc entry.
	objName := caaf.getObjName(fn)
	fsvc, err := caaf.fsCache.GetByFunctionUID(fn.UID)
	if err != nil {
		// Not in cache (never specialized, evicted, or executor restarted).
		// The backing object names are deterministic, so proceed with
		// cleanup using the computed name instead of failing — bailing out
		// here would leak the Deployment/Service/HPA.
		caaf.logger.V(1).Info("fsvc not in cache, cleaning up by computed name",
			"function", k8sCache.MetaObjectToName(fn), "obj_name", objName)
	} else {
		objName = fsvc.Name
		if _, err := caaf.fsCache.DeleteOld(fsvc, time.Second*0); err != nil {
			multierr = errors.Join(multierr, fmt.Errorf("error deleting function from cache: %w", err))
		}
	}

	// to support backward compatibility, if the function was created in default ns, we fall back to creating the
	// deployment of the function in fission-function ns, so cleaning up resources there
	ns := caaf.nsResolver.GetFunctionNS(fn.Namespace)

	multierr = errors.Join(multierr, caaf.cleanupContainer(ctx, ns, objName))
	return multierr
}

// getObjName returns a unique name for kubernetes objects of function
func (caaf *Container) getObjName(fn *fv1.Function) string {
	// use meta uuid of function, this ensure we always get the same name for the same function.
	uid := fn.UID[len(fn.UID)-17:]
	var functionMetadata string
	if len(fn.Name)+len(fn.Namespace) < 35 {
		functionMetadata = fn.Name + "-" + fn.Namespace
	} else {
		if len(fn.Name) > 17 {
			functionMetadata = fn.Name[:17]
		} else {
			functionMetadata = fn.Name
		}
		if len(fn.Namespace) > 17 {
			functionMetadata = functionMetadata + "-" + fn.Namespace[:17]
		} else {
			functionMetadata = functionMetadata + "-" + fn.Namespace
		}
	}
	// constructed name should be 63 characters long, as it is a valid k8s name
	// functionMetadata should be 35 characters long, as we take 17 characters from functionUid
	// with the "container-" 10 character prefix
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
	caaf.logger.Error(err, "function status update", "function", fn, "message", message)
}

// IdleStrategy returns the container idle-reaping strategy (scale the function
// deployment down to MinScale), run by the shared idle reaper. checkEnv is
// false: the container executor never inspected the environment list.
func (caaf *Container) IdleStrategy() idle.Strategy {
	return idle.NewScaleDownStrategy(caaf.logger, fv1.ExecutorTypeContainer, caaf.fissionClient,
		caaf.fsCache, caaf.kubernetesClient, caaf.defaultIdlePodReapTime, caaf.objectReaperIntervalSecond, false)
}

func (caaf *Container) DumpDebugInfo(ctx context.Context) error {
	return nil
}
