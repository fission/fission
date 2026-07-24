// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

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
	apiequality "k8s.io/apimachinery/pkg/api/equality"
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
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/throttler"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/maps"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

var (
	_ executortype.ExecutorType = &NewDeploy{}
)

type (
	// NewDeploy represents an ExecutorType
	NewDeploy struct {
		logger logr.Logger

		kubernetesClient kubernetes.Interface
		fissionClient    versioned.Interface
		instanceID       string
		fetcherConfig    *fetcherConfig.Config
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

		hpaops *hpautils.HpaOperations

		podSpecPatch               *apiv1.PodSpec
		objectReaperIntervalSecond time.Duration

		enableOwnerReferences bool

		// imageVolumeOK is the once-evaluated RFC-0001 Path B gate:
		// ENABLE_OCI_IMAGE_VOLUME opted in AND the cluster supports
		// KEP-4639 image volumes (>= 1.33).
		imageVolumeOK bool
	}
)

// MakeNewDeploy initializes and returns an instance of NewDeploy.
func MakeNewDeploy(
	ctx context.Context,
	logger logr.Logger,
	fissionClient versioned.Interface,
	kubernetesClient kubernetes.Interface,
	fetcherConfig *fetcherConfig.Config,
	instanceID string,
	podSpecPatch *apiv1.PodSpec,
) (executortype.ExecutorType, error) {
	enableIstio := false
	if len(os.Getenv("ENABLE_ISTIO")) > 0 {
		istio, err := strconv.ParseBool(os.Getenv("ENABLE_ISTIO"))
		if err != nil {
			logger.Error(err, "failed to parse 'ENABLE_ISTIO', set to false")
		}
		enableIstio = istio
	}

	// RFC-0001 Path B gate, evaluated once (shared helper, so poolmgr and
	// newdeploy cannot drift).
	imageVolumeOK := executorUtils.ImageVolumeGate(logger, kubernetesClient.Discovery())

	nd := &NewDeploy{
		logger: logger.WithName("new_deploy"),

		imageVolumeOK: imageVolumeOK,

		fissionClient:    fissionClient,
		kubernetesClient: kubernetesClient,
		instanceID:       instanceID,
		fsCache:          fscache.MakeFunctionServiceCache(logger),
		throttler:        throttler.MakeThrottler(1 * time.Minute),
		nsResolver:       utils.DefaultNSResolver(),

		fetcherConfig:          fetcherConfig,
		runtimeImagePullPolicy: utils.GetImagePullPolicy(os.Getenv("RUNTIME_IMAGE_PULL_POLICY")),
		useIstio:               enableIstio,

		defaultIdlePodReapTime:     2 * time.Minute,
		objectReaperIntervalSecond: time.Duration(executorUtils.GetObjectReaperInterval(logger, fv1.ExecutorTypeNewdeploy, 5)) * time.Second,
		hpaops:                     hpautils.NewHpaOperations(logger, kubernetesClient, instanceID),

		podSpecPatch: podSpecPatch,

		enableOwnerReferences: utils.IsOwnerReferencesEnabled(),
	}

	// The Function and Environment watches are controller-runtime reconcilers now
	// (see reconciler.go / RegisterReconcilers), wired on the executor Manager.
	// Deployment/Service reads (IsValid) go through the Manager's cache-backed
	// client (nd.crClient), set in RegisterReconcilers — no per-type informer
	// factory is needed.
	return nd, nil
}

// Run start the function and environment controller along with an object reaper.
// Run is a no-op: the newdeploy manager no longer runs its own informer
// factory. Its Deployment/Service reads (IsValid) go through the executor
// Manager's cache, which controller-runtime syncs before any runnable (including
// this type's reapers) starts.
func (deploy *NewDeploy) Run(context.Context, *errgroup.Group) {}

// GetTypeName returns the executor type name.
func (deploy *NewDeploy) GetTypeName(ctx context.Context) fv1.ExecutorType {
	return fv1.ExecutorTypeNewdeploy
}

// GetFuncSvc returns a function service; error otherwise.
func (deploy *NewDeploy) GetFuncSvc(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	return deploy.createFunction(ctx, fn)
}

// GetFuncSvcFromCache returns a function service from cache; error otherwise.
func (deploy *NewDeploy) GetFuncSvcFromCache(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	otelUtils.SpanTrackEvent(ctx, "GetFuncSvcFromCache")
	return deploy.fsCache.GetByFunctionUID(fn.UID)
}

// DeleteFuncSvcFromCache deletes a function service from cache.
func (deploy *NewDeploy) DeleteFuncSvcFromCache(ctx context.Context, fsvc *fscache.FuncSvc) {
	otelUtils.SpanTrackEvent(ctx, "DeleteFuncSvcFromCache")
	deploy.fsCache.DeleteEntry(fsvc)
}

// UnTapService has not been implemented for NewDeployment.
func (deploy *NewDeploy) UnTapService(ctx context.Context, fnMeta *metav1.ObjectMeta, svcHost string) {
	// Not Implemented for NewDeployment. Will be used when support of concurrent specialization of same function is added.
}

// MarkSpecializationFailure has not been implemented for NewDeployment.
func (deploy *NewDeploy) MarkSpecializationFailure(ctx context.Context, fnMeta *metav1.ObjectMeta) {
	// Not Implemented for NewDeployment. Will be used when support of concurrent specialization of same function is added.
}

// TapService makes a TouchByAddress request to the cache.
func (deploy *NewDeploy) TapService(ctx context.Context, svcHost string) error {
	otelUtils.SpanTrackEvent(ctx, "TapService")
	err := deploy.fsCache.TouchByAddress(svcHost)
	if err != nil {
		return err
	}
	return nil
}

// IsValid does a get on the service address to ensure it's a valid service, then
// scale deployment to 1 replica if there are no available replicas for function.
// Return true if no error occurs, return false otherwise.
func (deploy *NewDeploy) IsValid(ctx context.Context, fsvc *fscache.FuncSvc) bool {
	logger := otelUtils.LoggerWithTraceID(ctx, deploy.logger)
	otelUtils.SpanTrackEvent(ctx, "IsValid", fscache.GetAttributesForFuncSvc(fsvc)...)
	if len(strings.Split(fsvc.Address, ".")) == 0 {
		logger.Error(nil, "address not found in function service")
		return false
	}
	if len(fsvc.KubernetesObjects) == 0 {
		logger.Info("no kubernetes object related to function", "function", fsvc.Function.Name)
		return false
	}
	for _, obj := range fsvc.KubernetesObjects {
		objKey := client.ObjectKey{Namespace: obj.Namespace, Name: obj.Name}
		if strings.ToLower(obj.Kind) == "service" {
			if err := deploy.crClient.Get(ctx, objKey, &apiv1.Service{}); err != nil {
				if !k8sErrs.IsNotFound(err) {
					logger.Error(err, "error validating function service", "function", fsvc.Function.Name)
				}
				return false
			}

		} else if strings.ToLower(obj.Kind) == "deployment" {
			currentDeploy := &appsv1.Deployment{}
			if err := deploy.crClient.Get(ctx, objKey, currentDeploy); err != nil {
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
func (deploy *NewDeploy) RefreshFuncPods(ctx context.Context, logger logr.Logger, f fv1.Function) error {
	// Defence in depth for GHSA-cvw6-gfvv-953q — see fnCreate for context.
	if envNs := f.Spec.Environment.Namespace; envNs != "" && envNs != f.Namespace {
		return fmt.Errorf("cross-namespace environment reference is not allowed: fn.namespace=%s env.namespace=%s",
			f.Namespace, envNs)
	}

	env, err := deploy.fissionClient.CoreV1().Environments(f.Spec.Environment.Namespace).Get(ctx, f.Spec.Environment.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	funcLabels := deploy.getDeployLabels(f.ObjectMeta, metav1.ObjectMeta{
		Name:      f.Spec.Environment.Name,
		Namespace: f.Spec.Environment.Namespace,
		UID:       env.UID,
	})

	dep, err := deploy.kubernetesClient.AppsV1().Deployments(deploy.nsResolver.GetFunctionNS(f.ObjectMeta.Namespace)).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(funcLabels).AsSelector().String(),
	})

	if err != nil {
		return err
	}

	// Ideally there should be only one deployment but for now we rely on label/selector to ensure that condition
	for _, deployment := range dep.Items {
		rvCount, err := executorUtils.ReferencedResourcesRVSum(ctx, deploy.kubernetesClient, f.Namespace, f.Spec.Secrets, f.Spec.ConfigMaps)
		if err != nil {
			return err
		}

		patch := fmt.Sprintf(`{"spec" : {"template": {"spec":{"containers":[{"name": "%s", "image": "%s", "env":[{"name": "%s", "value": "%d"}]}]}}}}`,
			env.Name, env.Spec.Runtime.Image, fv1.ResourceVersionCount, rvCount)

		_, err = deploy.kubernetesClient.AppsV1().Deployments(deployment.ObjectMeta.Namespace).Patch(ctx, deployment.Name,
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
func (deploy *NewDeploy) AdoptExistingResources(ctx context.Context) {
	executorUtils.AdoptFunctions(ctx, deploy.logger, deploy.fissionClient, fv1.ExecutorTypeNewdeploy,
		func(ctx context.Context, fn *fv1.Function) error {
			_, err := deploy.createFunction(ctx, fn)
			return err
		})
}

// CleanupOldExecutorObjects cleans orphaned resources.
func (deploy *NewDeploy) CleanupOldExecutorObjects(ctx context.Context) {
	reaper.CleanupExecutorObjects(ctx, deploy.logger, deploy.kubernetesClient, deploy.instanceID, fv1.ExecutorTypeNewdeploy)
}

func (deploy *NewDeploy) getEnvFunctions(ctx context.Context, m *metav1.ObjectMeta) []fv1.Function {
	funcList, err := deploy.fissionClient.CoreV1().Functions(m.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		deploy.logger.Error(err, "Error getting functions for env", "environment", m)
	}
	relatedFunctions := make([]fv1.Function, 0)
	for _, f := range funcList.Items {
		if (f.Spec.Environment.Name == m.Name) && (f.Spec.Environment.Namespace == m.Namespace) && f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypeNewdeploy {
			relatedFunctions = append(relatedFunctions, f)
		}
	}
	return relatedFunctions
}

// updateEnvFunctions rebuilds the deployments of all newdeploy functions of the
// given environment after its runtime image changed. It drives the Environment
// reconciler (replacing the EnvEventHandlers UpdateFunc body). A per-function
// failure is logged and skipped rather than failing the whole environment sweep:
// requeuing the environment would re-roll every (including already-updated)
// function, which is the amplification the informer handler avoided.
func (deploy *NewDeploy) updateEnvFunctions(ctx context.Context, env *fv1.Environment) error {
	deploy.logger.V(1).Info("updating functions of environment with changed image", "environment", env.ObjectMeta)
	for _, f := range deploy.getEnvFunctions(ctx, &env.ObjectMeta) {
		function, err := deploy.fissionClient.CoreV1().Functions(f.Namespace).Get(ctx, f.Name, metav1.GetOptions{})
		if err != nil {
			deploy.logger.Error(err, "error getting function while updating environment functions", "function", f.ObjectMeta)
			continue
		}
		if err := deploy.updateFuncDeployment(ctx, function, env); err != nil {
			deploy.logger.Error(err, "error updating function deployment after environment image change", "function", function.ObjectMeta)
			continue
		}
	}
	return nil
}

// resourcesExist reports whether the function's backing Deployment and Service
// are present in the Manager cache. A missing object means it drifted away
// (deleted out-of-band) and the function should be recreated. Reads go through
// the cache-backed crClient (same path as IsValid).
func (deploy *NewDeploy) resourcesExist(ctx context.Context, fn *fv1.Function) (bool, error) {
	ns := deploy.nsResolver.GetFunctionNS(fn.Namespace)
	key := client.ObjectKey{Namespace: ns, Name: deploy.getObjName(fn)}
	for _, obj := range []client.Object{&appsv1.Deployment{}, &apiv1.Service{}} {
		if err := deploy.crClient.Get(ctx, key, obj); err != nil {
			if k8sErrs.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

func (deploy *NewDeploy) createFunction(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	if fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeNewdeploy {
		return nil, nil
	}

	logger := otelUtils.LoggerWithTraceID(ctx, deploy.logger)

	fsvcObj, err := deploy.throttler.RunOnce(string(fn.UID), func(ableToCreate bool) (any, error) {
		if ableToCreate {
			return deploy.fnCreate(ctx, fn)
		}
		return deploy.fsCache.GetByFunctionUID(fn.UID)
	})

	if err != nil {
		e := "error creating k8s resources for function"
		logger.Error(err, e, "function_name", fn.Name,
			"function_namespace", fn.Namespace)
		return nil, fmt.Errorf("error creating k8s resources for function %s: %w", k8sCache.MetaObjectToName(fn), err)
	}

	fsvc, ok := fsvcObj.(*fscache.FuncSvc)
	if !ok {
		logger.Error(nil, "receive unknown object while creating function - expected pointer of function service object")

		panic("receive unknown object while creating function - expected pointer of function service object")
	}
	otelUtils.SpanTrackEvent(ctx, "fnSvcResponse", fscache.GetAttributesForFuncSvc(fsvc)...)

	return fsvc, err
}

func (deploy *NewDeploy) deleteFunction(ctx context.Context, fn *fv1.Function) error {
	if fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeNewdeploy {
		return nil
	}
	err := deploy.fnDelete(ctx, fn)
	if err != nil {
		return fmt.Errorf("error deleting kubernetes objects of function %s: %w", k8sCache.MetaObjectToName(fn), err)
	}
	return nil
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

func (deploy *NewDeploy) fnCreate(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	// Defence in depth for GHSA-cvw6-gfvv-953q — primary defence is the
	// admission webhook in pkg/webhook/function.go, but a stale Function
	// from a pre-webhook upgrade window (or failurePolicy=ignore) could
	// still reach this path.
	if envNs := fn.Spec.Environment.Namespace; envNs != "" && envNs != fn.Namespace {
		return nil, fmt.Errorf("cross-namespace environment reference is not allowed: fn.namespace=%s env.namespace=%s",
			fn.Namespace, envNs)
	}

	// Authoritative re-read: the Function object reaching this path originates
	// from the router's request body and can be stale — its DeletionTimestamp
	// may be absent even though the Function is being deleted. Without this
	// check a create can race the delete teardown (funcreconciler) and
	// re-create the Deployment/Service/HPA *after* teardown removed them,
	// leaking objects whose owning Function CR is already gone.
	live, err := deploy.fissionClient.CoreV1().Functions(fn.Namespace).Get(ctx, fn.Name, metav1.GetOptions{})
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

	cleanupFunc := func(ctx context.Context, ns string, name string) {
		err := deploy.cleanupNewdeploy(ctx, ns, name)
		if err != nil {
			deploy.logger.Error(err, "received error while cleaning function resources",
				"namespace", ns, "name", name)
		}
	}
	env, err := deploy.fissionClient.CoreV1().
		Environments(fn.Spec.Environment.Namespace).
		Get(ctx, fn.Spec.Environment.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	objName := deploy.getObjName(fn)
	deployLabels := deploy.getDeployLabels(fn.ObjectMeta, env.ObjectMeta)
	deployAnnotations := deploy.getDeployAnnotations(fn.ObjectMeta, env.ObjectMeta)

	// to support backward compatibility, if the function was created in default ns, we fall back to creating the
	// deployment of the function in fission-function ns
	ns := deploy.nsResolver.GetFunctionNS(fn.Namespace)

	// Envoy(istio-proxy) returns 404 directly before istio pilot
	// propagates latest Envoy-specific configuration.
	// Since newdeploy waits for pods of deployment to be ready,
	// change the order of kubeObject creation (create service first,
	// then deployment) to take advantage of waiting time.
	// Transient executor errors are not written to Function.Status; see
	// the analogous note in pkg/executor/executortype/poolmgr/gp.go.
	svc, err := deploy.createOrGetSvc(ctx, fn, deployLabels, deployAnnotations, objName, ns)
	if err != nil {
		deploy.logger.Error(err, "error creating service", "service", objName)
		if destroyOnCreateError(err) {
			go cleanupFunc(context.Background(), ns, objName)
		}
		return nil, fmt.Errorf("error creating service %s: %w", objName, err)
	}
	svcAddress := fmt.Sprintf("%s.%s", svc.Name, svc.Namespace)

	depl, err := deploy.createOrGetDeployment(ctx, fn, env, objName, deployLabels, deployAnnotations, ns)
	if err != nil {
		deploy.logger.Error(err, "error creating deployment", "deployment", objName)
		if destroyOnCreateError(err) {
			go cleanupFunc(context.Background(), ns, objName)
		}
		return nil, fmt.Errorf("error creating deployment %s: %w", objName, err)
	}

	// env was already validated by createOrGetDeployment above, so the
	// container name resolves without error here.
	mainContainerName, err := deploy.mainContainerName(env)
	if err != nil {
		if destroyOnCreateError(err) {
			go cleanupFunc(context.Background(), ns, objName)
		}
		return nil, fmt.Errorf("error resolving main container for HPA %s: %w", objName, err)
	}

	hpa, err := deploy.hpaops.CreateOrGetHpa(ctx, fn, objName, &fn.Spec.InvokeStrategy.ExecutionStrategy, mainContainerName, depl, deployLabels, deployAnnotations)
	if err != nil {
		deploy.logger.Error(err, "error creating HPA", "hpa", objName)
		if destroyOnCreateError(err) {
			go cleanupFunc(context.Background(), ns, objName)
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
		Environment:       env,
		Address:           svcAddress,
		KubernetesObjects: kubeObjRefs,
		Executor:          fv1.ExecutorTypeNewdeploy,
	}

	_, err = deploy.fsCache.Add(*fsvc)
	if err != nil {
		deploy.logger.Error(err, "error adding function to cache", "function", fsvc.Function)
		metrics.RecordColdStartError(ctx, fn.Name, fn.Namespace)
		return fsvc, err
	}

	metrics.RecordColdStart(ctx, fn.Name, fn.Namespace)
	executorUtils.SetFunctionReady(ctx, deploy.logger, deploy.fissionClient, fn, fv1.FunctionReasonReady, "newdeploy deployment is ready")

	return fsvc, nil
}

func (deploy *NewDeploy) updateFunction(ctx context.Context, oldFn *fv1.Function, newFn *fv1.Function) error {

	if oldFn.ResourceVersion == newFn.ResourceVersion {
		return nil
	}

	// Ignoring updates to functions which are not of NewDeployment type
	if newFn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeNewdeploy &&
		oldFn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeNewdeploy {
		return nil
	}

	// Executor type is no longer New Deployment
	if newFn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeNewdeploy &&
		oldFn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypeNewdeploy {
		deploy.logger.Info("function does not use new deployment executor anymore, deleting resources",
			"function", newFn)
		// IMP - pass the oldFn, as the new/modified function is not in cache
		return deploy.deleteFunction(ctx, oldFn)
	}

	// Executor type changed to New Deployment from something else
	if oldFn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeNewdeploy &&
		newFn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypeNewdeploy {
		deploy.logger.Info("function type changed to new deployment, creating resources",
			"old_function", oldFn.ObjectMeta,
			"new_function", newFn.ObjectMeta)
		_, err := deploy.createFunction(ctx, newFn)
		if err != nil {
			deploy.updateStatus(oldFn, err, "error changing the function's type to newdeploy")
		}
		return err
	}

	deployChanged := false

	if !reflect.DeepEqual(oldFn.Spec.InvokeStrategy, newFn.Spec.InvokeStrategy) {

		// to support backward compatibility, if the function was created in default ns, we fall back to creating the
		// deployment of the function in fission-function ns, so cleaning up resources there
		ns := deploy.nsResolver.GetFunctionNS(newFn.Namespace)

		fsvc, err := deploy.fsCache.GetByFunctionUID(newFn.UID)
		if err != nil {
			return fmt.Errorf("error updating function due to unable to find function service cache %s: %w", k8sCache.MetaObjectToName(oldFn), err)
		}

		hpa, err := deploy.hpaops.GetHpa(ctx, ns, fsvc.Name)
		if err != nil {
			deploy.updateStatus(oldFn, err, "error getting HPA while updating function")
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
			env, err := deploy.fissionClient.CoreV1().Environments(newFn.Spec.Environment.Namespace).
				Get(ctx, newFn.Spec.Environment.Name, metav1.GetOptions{})
			if err != nil {
				deploy.updateStatus(oldFn, err, "failed to get environment while updating HPA metrics")
				return err
			}
			mainContainerName, err := deploy.mainContainerName(env)
			if err != nil {
				deploy.updateStatus(oldFn, err, "failed to resolve main container while updating HPA metrics")
				return err
			}
			hpa.Spec.Metrics = hpautils.RewriteResourceMetricsToContainer(
				newFn.Spec.InvokeStrategy.ExecutionStrategy.Metrics, mainContainerName, deploy.logger)
			hpaChanged = true
		}

		if !reflect.DeepEqual(newFn.Spec.InvokeStrategy.ExecutionStrategy.Behavior, oldFn.Spec.InvokeStrategy.ExecutionStrategy.Behavior) {
			hpa.Spec.Behavior = newFn.Spec.InvokeStrategy.ExecutionStrategy.Behavior
			hpaChanged = true
		}

		if hpaChanged {
			err := deploy.hpaops.UpdateHpa(ctx, hpa)
			if err != nil {
				deploy.updateStatus(oldFn, err, "error updating HPA while updating function")
				return err
			}
		}
	}

	if oldFn.Spec.Environment != newFn.Spec.Environment ||
		oldFn.Spec.Package.PackageRef != newFn.Spec.Package.PackageRef ||
		oldFn.Spec.Package.FunctionName != newFn.Spec.Package.FunctionName {
		deploy.logger.V(1).Info("deployment changed", "msg", "deployment changed")
		deployChanged = true
	}

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

	if deployChanged {
		env, err := deploy.fissionClient.CoreV1().Environments(newFn.Spec.Environment.Namespace).
			Get(ctx, newFn.Spec.Environment.Name, metav1.GetOptions{})
		if err != nil {
			deploy.updateStatus(oldFn, err, "failed to get environment while updating function")
			return err
		}
		return deploy.updateFuncDeployment(ctx, newFn, env)
	}

	return nil
}

// reconcileDeploymentSpec brings an already-existing deployment up to the
// function's current spec when it lags. createFunction only *adopts/scales* an
// existing deployment (it does not rewrite the pod spec), and updateFunction is
// diff-based against the last-reconciled object. So if a function's create and a
// later spec update coalesce into a single first reconcile — common when the
// router specializes the function on-demand (creating the deployment) just before
// `fission fn update` lands — the deployment can be left on the old spec with no
// transition for updateFunction to diff. The deployment carries the function's
// ResourceVersion as a metadata annotation (getDeployAnnotations), so compare it:
// if stale, push the current spec. A no-op when already current.
func (deploy *NewDeploy) reconcileDeploymentSpec(ctx context.Context, fn *fv1.Function) error {
	fsvc, err := deploy.fsCache.GetByFunctionUID(fn.UID)
	if err != nil {
		// Not specialized yet — no deployment to reconcile; the on-demand path will
		// create it from the current spec when the function is first invoked.
		return nil
	}
	ns := deploy.nsResolver.GetFunctionNS(fn.Namespace)
	existingDepl, err := deploy.kubernetesClient.AppsV1().Deployments(ns).Get(ctx, fsvc.Name, metav1.GetOptions{})
	if err != nil {
		if k8sErrs.IsNotFound(err) {
			return nil
		}
		return err
	}
	if existingDepl.Annotations[fv1.FUNCTION_RESOURCE_VERSION] == fn.ResourceVersion {
		return nil // deployment already reflects the current function spec
	}
	env, err := deploy.fissionClient.CoreV1().Environments(fn.Spec.Environment.Namespace).
		Get(ctx, fn.Spec.Environment.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	deploy.logger.Info("reconciling stale deployment to current function spec on first sight",
		"function", fn.Name, "deployment", fsvc.Name,
		"deployment_rv", existingDepl.Annotations[fv1.FUNCTION_RESOURCE_VERSION], "function_rv", fn.ResourceVersion)
	return deploy.updateFuncDeployment(ctx, fn, env)
}

func (deploy *NewDeploy) updateFuncDeployment(ctx context.Context, fn *fv1.Function, env *fv1.Environment) error {
	fsvc, err := deploy.fsCache.GetByFunctionUID(fn.UID)
	if err != nil {
		return fmt.Errorf("error updating function due to unable to find function service cache: %s: %w", k8sCache.MetaObjectToName(fn), err)
	}
	fnObjName := fsvc.Name

	deployLabels := deploy.getDeployLabels(fn.ObjectMeta, env.ObjectMeta)
	deploy.logger.Info("updating deployment due to function/environment update",
		"deployment", fnObjName, "function", fn.Name)

	// to support backward compatibility, if the function was created in default ns, we fall back to creating the
	// deployment of the function in fission-function ns
	ns := deploy.nsResolver.GetFunctionNS(fn.Namespace)

	deployAnnotations := deploy.getDeployAnnotations(fn.ObjectMeta, env.ObjectMeta)

	existingDepl, err := deploy.kubernetesClient.AppsV1().Deployments(ns).Get(ctx, fnObjName, metav1.GetOptions{})
	if err != nil {
		if k8sErrs.IsNotFound(err) {
			// The deployment is gone (e.g. raced with a delete). There is nothing to
			// update in place; the next on-demand specialization recreates it. Return
			// nil rather than an error, which would requeue forever against a missing
			// object. This matches the old informer handler, which logged the Get
			// error and returned.
			deploy.logger.Info("deployment not found while updating function; skipping in-place update",
				"deployment", fnObjName, "function", fn.Name)
			return nil
		}
		return err
	}

	// the resource version inside function packageRef is changed,
	// so the content of fetchRequest in deployment cmd is different.
	// Therefore, the deployment update will trigger a rolling update.
	newDeployment, err := deploy.getDeploymentSpec(ctx, fn, env,
		existingDepl.Spec.Replicas, // use current replicas instead of minscale in the ExecutionStrategy.
		fnObjName, ns, deployLabels, deployAnnotations)
	if err != nil {
		deploy.updateStatus(fn, err, "failed to get new deployment spec while updating function")
		return err
	}

	// A Deployment's selector is immutable. It is stable across a function's
	// code/secret/HPA updates, but changes when the function's environment
	// reference changes (the environment UID is part of the selector labels). An
	// in-place Update with a different selector is rejected by the API server, so
	// skip the rebuild and leave the existing pods serving — matching the old
	// informer handler, which logged the rejected Update and moved on. Returning
	// nil (not an error) avoids requeuing forever against a permanently immutable
	// field.
	if !apiequality.Semantic.DeepEqual(existingDepl.Spec.Selector, newDeployment.Spec.Selector) {
		deploy.logger.Info("deployment selector changed (e.g. environment reference changed); cannot update in place, leaving existing deployment",
			"deployment", fnObjName, "function", fn.Name)
		return nil
	}

	err = deploy.updateDeployment(ctx, newDeployment, ns)
	if err != nil {
		deploy.updateStatus(fn, err, "failed to update deployment while updating function")
		return err
	}

	return nil
}

func (deploy *NewDeploy) fnDelete(ctx context.Context, fn *fv1.Function) error {
	var errs error

	// GetByFunction now keys on UID+Generation (see #3596), not
	// ResourceVersion, but the fn passed in on delete can still carry a
	// Generation the cache entry was never keyed under (e.g. a delete
	// racing a spec update, or a stale informer snapshot) — GetByFunctionUID
	// is the UID-only lookup that doesn't depend on the caller's metadata
	// matching the cached entry's, so it's used here to find the correct
	// fsvc entry.
	objName := deploy.getObjName(fn)
	fsvc, err := deploy.fsCache.GetByFunctionUID(fn.UID)
	if err != nil {
		// Not in cache (never specialized, evicted, or executor restarted).
		// The backing object names are deterministic, so proceed with
		// cleanup using the computed name instead of failing — bailing out
		// here would leak the Deployment/Service/HPA.
		deploy.logger.V(1).Info("fsvc not in cache, cleaning up by computed name",
			"function", k8sCache.MetaObjectToName(fn), "obj_name", objName)
	} else {
		objName = fsvc.Name
		if _, err := deploy.fsCache.DeleteOld(fsvc, time.Second*0); err != nil {
			errs = errors.Join(errs, fmt.Errorf("error deleting the function from cache"))
		}
	}

	// to support backward compatibility, if the function was created in default ns, we fall back to creating the
	// deployment of the function in fission-function ns, so cleaning up resources there
	ns := deploy.nsResolver.GetFunctionNS(fn.Namespace)

	errs = errors.Join(errs, deploy.cleanupNewdeploy(ctx, ns, objName))

	return errs
}

// getObjName returns a unique name for kubernetes objects of function
func (deploy *NewDeploy) getObjName(fn *fv1.Function) string {
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
	// with newdeploy 10 character prefix
	return strings.ToLower(fmt.Sprintf("newdeploy-%s-%s", functionMetadata, uid))
}

func (deploy *NewDeploy) getDeployLabels(fnMeta metav1.ObjectMeta, envMeta metav1.ObjectMeta) map[string]string {
	deployLabels := map[string]string{
		fv1.EXECUTOR_TYPE:         string(fv1.ExecutorTypeNewdeploy),
		fv1.ENVIRONMENT_NAME:      envMeta.Name,
		fv1.ENVIRONMENT_NAMESPACE: envMeta.Namespace,
		fv1.ENVIRONMENT_UID:       string(envMeta.UID),
		fv1.FUNCTION_NAME:         fnMeta.Name,
		fv1.FUNCTION_NAMESPACE:    fnMeta.Namespace,
		fv1.FUNCTION_UID:          string(fnMeta.UID),
	}
	maps.MergeStringMap(deployLabels, envMeta.Labels)
	maps.MergeStringMap(deployLabels, fnMeta.Labels)
	return deployLabels
}

func (deploy *NewDeploy) getDeployAnnotations(fnMeta metav1.ObjectMeta, envMeta metav1.ObjectMeta) map[string]string {
	deployAnnotations := maps.CopyStringMap(envMeta.Annotations)
	maps.MergeStringMap(deployAnnotations, fnMeta.Annotations)
	deployAnnotations[fv1.EXECUTOR_INSTANCEID_LABEL] = deploy.instanceID
	deployAnnotations[fv1.FUNCTION_RESOURCE_VERSION] = fnMeta.ResourceVersion
	return deployAnnotations
}

// updateStatus is a function which updates status of update.
// Current implementation only logs messages, in future it will update function status
func (deploy *NewDeploy) updateStatus(fn *fv1.Function, err error, message string) {
	deploy.logger.Error(nil, "function status update", "function", fn, "message", message)
}

// IdleStrategy returns the newdeploy idle-reaping strategy (scale the function
// deployment down to MinScale), run by the shared idle reaper. checkEnv is true
// so it mirrors the previous behaviour of logging function services whose
// environment was deleted and skipping the pass on an environment-list error.
func (deploy *NewDeploy) IdleStrategy() idle.Strategy {
	return idle.NewScaleDownStrategy(deploy.logger, fv1.ExecutorTypeNewdeploy, deploy.fissionClient,
		deploy.fsCache, deploy.kubernetesClient, deploy.defaultIdlePodReapTime, deploy.objectReaperIntervalSecond, true)
}

func (deploy *NewDeploy) DumpDebugInfo(ctx context.Context) error {
	return nil
}
