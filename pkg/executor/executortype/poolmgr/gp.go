// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dchest/uniuri"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	k8s_err "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"k8s.io/client-go/util/workqueue"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/fscache"
	executorUtil "github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/svcinfo"
	"github.com/fission/fission/pkg/utils"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

type (
	// GenericPool represents a generic environment pool
	GenericPool struct {
		logger                 logr.Logger
		lock                   sync.Mutex
		env                    *fv1.Environment
		deployment             *appsv1.Deployment            // kubernetes deployment
		fnNamespace            string                        // namespace to keep our resources
		podReadyTimeout        time.Duration                 // timeout for generic pods to become ready
		fsCache                *fscache.FunctionServiceCache // cache funcSvc's by function, address and podname
		useSvc                 bool                          // create k8s service for specialized pods
		useIstio               bool
		runtimeImagePullPolicy apiv1.PullPolicy // pull policy for generic pool to created env deployment
		kubernetesClient       kubernetes.Interface
		metricsClient          metricsclient.Interface
		fissionClient          versioned.Interface
		fetcherConfig          *fetcherConfig.Config
		cancelPoolCtx          context.CancelFunc
		// crClient is the executor Manager's cache-backed client; choosePod reads
		// warm pods from it (replacing the per-pool readyPod informer's lister).
		crClient              client.Client
		readyPodQueue         workqueue.TypedDelayingInterface[string]
		poolInstanceID        string // small random string to uniquify pod names
		instanceID            string // poolmgr instance id
		podSpecPatch          *apiv1.PodSpec
		enableOwnerReferences bool
		// oci marks this as a per-image image-volume pool (RFC-0001 Path B):
		// its pods mount the package image read-only at the fetcher's store
		// path (<sharedMountPath>/deployarchive). nil for plain pools.
		oci *fv1.OCIArchive
		// ociFetcherVariant selects the fetcher-retained Path B variant
		// (RFC-0012 "B-fetcher"): the fetcher sidecar stays in the pod to
		// materialize Secrets/ConfigMaps and drive the load; its
		// exists-early-exit makes the fetch a no-op against the image mount.
		// false = "B-direct" (no fetcher; load-only specialize).
		ociFetcherVariant bool
		// ociImageHash is ociPoolHash(oci): keys the pool, labels its
		// pods, and suffixes the deployment name. Empty for plain pools.
		ociImageHash string
		// lastActive (unix nanos) is the pool's activity clock for the
		// per-image idle reaper (RFC-0012): stored at creation and on every
		// GET_POOL. Atomic so the reap pass can read it lock-free.
		lastActive atomic.Int64
		// TODO: move this field into fsCache
		podFSVCMap sync.Map
	}
)

// podReadyTimeoutFromEnv parses POD_READY_TIMEOUT, defaulting to 300s on a
// missing or unparsable value. Called once by MakeGenericPoolManager rather
// than on every pool creation.
func podReadyTimeoutFromEnv(logger logr.Logger) time.Duration {
	podReadyTimeoutStr := os.Getenv("POD_READY_TIMEOUT")
	podReadyTimeout, err := time.ParseDuration(podReadyTimeoutStr)
	if err != nil {
		podReadyTimeout = 300 * time.Second
		logger.Error(err, "failed to parse pod ready timeout duration from 'POD_READY_TIMEOUT' - set to the default value",
			"value", podReadyTimeoutStr,
			"default", podReadyTimeout)
	}
	return podReadyTimeout
}

// MakeGenericPool returns an instance of GenericPool
func MakeGenericPool(
	logger logr.Logger,
	fissionClient versioned.Interface,
	kubernetesClient kubernetes.Interface,
	metricsClient metricsclient.Interface,
	env *fv1.Environment,
	fnNamespace string,
	fsCache *fscache.FunctionServiceCache,
	fetcherConfig *fetcherConfig.Config,
	instanceID string,
	enableIstio bool,
	podSpecPatch *apiv1.PodSpec,
	crClient client.Client,
	oci *ociPoolSpec,
	podReadyTimeout time.Duration) *GenericPool {

	gpLogger := logger.WithName("generic_pool")

	gpLogger.Info("creating pool", "environment", env)

	// TODO: in general we need to provide the user a way to configure pools.  Initial
	// replicas, autoscaling params, various timeouts, etc.
	gp := &GenericPool{
		logger:                gpLogger,
		env:                   env,
		fissionClient:         fissionClient,
		kubernetesClient:      kubernetesClient,
		metricsClient:         metricsClient,
		fnNamespace:           fnNamespace,
		podReadyTimeout:       podReadyTimeout,
		fsCache:               fsCache,
		fetcherConfig:         fetcherConfig,
		useSvc:                false,       // defaults off -- svc takes a second or more to become routable, slowing cold start
		useIstio:              enableIstio, // defaults off -- istio integration requires pod relabeling and it takes a second or more to become routable, slowing cold start
		crClient:              crClient,
		readyPodQueue:         workqueue.NewTypedDelayingQueueWithConfig(workqueue.TypedDelayingQueueConfig[string]{Name: "readyPodQueue"}),
		poolInstanceID:        uniuri.NewLen(8),
		instanceID:            instanceID,
		podFSVCMap:            sync.Map{},
		podSpecPatch:          podSpecPatch,
		enableOwnerReferences: utils.IsOwnerReferencesEnabled(),
		lock:                  sync.Mutex{},
	}
	if oci != nil {
		gp.oci = oci.archive
		gp.ociFetcherVariant = oci.fetcherVariant
		gp.ociImageHash = ociPoolHash(oci)
	}
	gp.lastActive.Store(time.Now().UnixNano())

	gp.runtimeImagePullPolicy = utils.GetImagePullPolicy(os.Getenv("RUNTIME_IMAGE_PULL_POLICY"))

	return gp
}

func (gp *GenericPool) setup(ctx context.Context) error {
	// create the pool
	err := gp.createPoolDeployment(ctx, gp.env)
	if err != nil {
		return err
	}
	// Warm pods of this pool are fed into gp.readyPodQueue by the executor's
	// Pod reconciler (see reconciler.go); choosePod consumes from it.
	// updateCPUUtilizationSvc runs for the lifetime of the pool, so it gets a
	// context tied to the pool (cancelled in destroy) rather than the
	// request-scoped ctx, which would cancel once the triggering request ends.
	poolCtx, cancel := context.WithCancel(context.Background())
	gp.cancelPoolCtx = cancel
	go gp.updateCPUUtilizationSvc(poolCtx)
	return nil
}

func (gp *GenericPool) getFuncSvc(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	logger := otelUtils.LoggerWithTraceID(ctx, gp.logger).WithValues("function", fn.Name, "namespace", fn.Namespace,
		"env", fn.Spec.Environment.Name, "envNamespace", fn.Spec.Environment.Namespace)

	logger.Info("choosing pod from pool")
	funcLabels := gp.labelsForFunction(&fn.ObjectMeta)

	if gp.useIstio {
		// Istio only allows accessing pod through k8s service, and requests come to
		// service are not always being routed to the same pod. For example:

		// If there is only one pod (podA) behind the service svcX.

		// svcX -> podA

		// All requests (specialize request & function access requests)
		// will be routed to podA without any problem.

		// If podA and podB are behind svcX.

		// svcX -> podA (specialized)
		//      -> podB (non-specialized)

		// The specialize request may be routed to podA and the function access
		// requests may go to podB. In this case, the function cannot be served
		// properly.

		// To prevent such problem, we need to delete old versions function pods
		// and make sure that there is only one pod behind the service

		sel := map[string]string{
			"functionName": fn.Name,
			"functionUid":  string(fn.UID),
		}
		podList, err := gp.kubernetesClient.CoreV1().Pods(gp.fnNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: labels.Set(sel).AsSelector().String(),
		})
		if err != nil {
			return nil, err
		}

		// Remove old versions function pods
		for _, pod := range podList.Items {
			// Delete pod no matter what status it is
			gp.kubernetesClient.CoreV1().Pods(gp.fnNamespace).Delete(ctx, pod.ObjectMeta.Name, metav1.DeleteOptions{}) // nolint errcheck
		}
	}

	// The relabel carries the function-generation label (RFC-0002) on top of
	// funcLabels; list/selection paths (RefreshFuncPods, the legacy
	// useSvc/istio selectors below) keep the generation-agnostic funcLabels.
	key, pod, err := gp.choosePod(ctx, gp.specializedPodLabels(&fn.ObjectMeta))
	if err != nil {
		// Transient executor errors (ChoosePodFailed, specialize timeout,
		// etc.) are NOT written to Function.Status.Conditions: getFuncSvc
		// runs on the cold-start hot path and may see many transient
		// failures in quick succession. Status flapping there is noisy
		// and not useful — those signals belong in logs / metrics
		// (already covered by metrics.RecordColdStartError). Status only
		// transitions on durable state (specialized successfully, or
		// the buildermgr reporting a permanent PackageBuildFailed).
		return nil, err
	}
	gp.readyPodQueue.Done(key)
	// NOTE: we don't write EnvironmentConditionReady here. Status
	// updates would bump env.ResourceVersion, which the buildermgr
	// uses to compose the builder service hostname (see
	// pkg/buildermgr/common.go.buildPackage) — racing the RV bump
	// against an in-flight source-archive build manifests as
	// "no such host" DNS errors. Decoupling that name from RV is
	// follow-up work.
	err = gp.specializePod(ctx, pod, fn)
	if err != nil {
		go gp.scheduleDeletePod(context.Background(), pod.Name)
		return nil, err
	}
	logger.Info("specialized pod", "pod", pod.Name, "podNamespace", pod.Namespace, "podIP", pod.Status.PodIP)

	var svcHost string
	if gp.useSvc && !gp.useIstio {
		svcName := fmt.Sprintf("svc-%s", fn.Name)
		if len(fn.UID) > 0 {
			svcName = fmt.Sprintf("%s-%v", svcName, fn.UID)
		}

		svc, err := gp.createSvc(ctx, svcName, funcLabels)
		if err != nil {
			go gp.scheduleDeletePod(context.Background(), pod.Name)
			return nil, err
		}
		if svc.Name != svcName {
			go gp.scheduleDeletePod(context.Background(), pod.Name)
			return nil, fmt.Errorf("sanity check failed for svc %s", svc.Name)
		}

		// the fission router isn't in the same namespace, so return a
		// namespace-qualified hostname
		svcHost = fmt.Sprintf("%v.%v:%d", svcName, gp.fnNamespace, svcinfo.PortEnvRuntime)
	} else if gp.useIstio {
		svc := utils.GetFunctionIstioServiceName(fn.Name, fn.Namespace)
		svcHost = fmt.Sprintf("%v.%v:%d", svc, gp.fnNamespace, svcinfo.PortEnvRuntime)
	} else {
		svcHost = fmt.Sprintf("%v:%d", pod.Status.PodIP, svcinfo.PortEnvRuntime)
	}

	otelUtils.SpanTrackEvent(ctx, "addFunctionLabel", otelUtils.GetAttributesForPod(pod)...)

	// kubeObjRefs is built from the pre-patch pod: the svc-host/served patch below
	// runs asynchronously, and IsValid matches the cached pod by name + PodIP, not
	// by the ResourceVersion captured here.
	kubeObjRefs := []apiv1.ObjectReference{
		{
			Kind:            "pod",
			Name:            pod.Name,
			APIVersion:      pod.APIVersion,
			Namespace:       pod.Namespace,
			ResourceVersion: pod.ResourceVersion,
			UID:             pod.UID,
		},
	}
	cpuUsage := resource.MustParse("0m")
	for _, container := range pod.Spec.Containers {
		val := *container.Resources.Limits.Cpu()
		cpuUsage.Add(val)
	}

	// set cpuLimit to 85th percentage of the cpuUsage
	cpuLimit, err := gp.getPercent(cpuUsage, 0.85)
	if err != nil {
		logger.Error(err, "failed to get 85 of CPU usage")
		cpuLimit = cpuUsage
	}
	logger.V(1).Info("cpuLimit set to", "cpulimit", cpuLimit)

	m := fn.ObjectMeta // only cache necessary part
	fsvc := &fscache.FuncSvc{
		Name:              pod.Name,
		Function:          &m,
		Environment:       gp.env,
		Address:           svcHost,
		KubernetesObjects: kubeObjRefs,
		Executor:          fv1.ExecutorTypePoolmgr,
		CPULimit:          cpuLimit,
		Ctime:             time.Now(),
		Atime:             time.Now(),
	}

	gp.fsCache.PodToFsvc.Store(pod.GetObjectMeta().GetName(), fsvc)
	gp.podFSVCMap.Store(pod.Name, []any{crd.CacheKeyUGFromMeta(fsvc.Function), fsvc.Address})
	gp.fsCache.AddFunc(ctx, *fsvc, fn.GetRequestPerPod(), fn.GetRetainPods())

	logger.Info("added function service",
		"pod", pod.Name,
		"podNamespace", pod.Namespace,
		"serviceHost", svcHost,
		"podIP", pod.Status.PodIP)

	otelUtils.SpanTrackEvent(ctx, "getFuncSvcComplete", fscache.GetAttributesForFuncSvc(fsvc)...)

	// The address handed to the router (svcHost) and the cached fsvc are complete,
	// so the two best-effort writes below are moved OFF the cold-start path —
	// synchronously they added ~20-50ms to every first cold start of a (function,
	// generation). runDetached runs each past the (about-to-be-cancelled) RPC ctx
	// on a deadline-bounded context, with a panic guard.
	podName, podNS, fnRV := pod.Name, pod.Namespace, fn.ResourceVersion

	// (1) Patch the pod's svc-host + function-RV annotations (so a restarted
	// executor can adopt it) and the served label (RFC-0002: admits the pod into
	// its function Service's EndpointSlices for subsequent warm requests). The
	// cold-start request itself uses the RPC-returned address, not the slice, so
	// deferring this off the response path doesn't affect the cold start. This
	// goroutine is the SOLE writer of the served label, and a restarted executor
	// adopts the pod from these annotations — so a dropped patch would keep the
	// pod off its slice (warm requests falling back to the executor RPC) for the
	// pod's life. Retry transient errors on the bounded backoff; give up only when
	// the pod is gone (NotFound). A restart inside the now-short write window still
	// self-heals — the next request just pays a fresh cold start.
	gp.runDetached(ctx, "patch svc-host/served label on pod "+podName, func(dctx context.Context) {
		patch := fmt.Sprintf(`{"metadata":{"annotations":{"%s":"%s","%s":"%s"},"labels":{"%s":"%s"}}}`,
			fv1.ANNOTATION_SVC_HOST, svcHost, fv1.FUNCTION_RESOURCE_VERSION, fnRV, fv1.SERVED_LABEL, fv1.SERVED_VALUE)
		err := retry.OnError(retry.DefaultBackoff, func(err error) bool { return !k8s_err.IsNotFound(err) }, func() error {
			_, perr := gp.kubernetesClient.CoreV1().Pods(podNS).Patch(dctx, podName, k8sTypes.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
			return perr
		})
		if err != nil {
			logger.Error(err, "error patching svc-host to pod after retries", "pod", podName, "ns", podNS)
		}
	})

	// (2) Mark the function Ready: a best-effort status condition that nothing in
	// the request path or the returned fsvc reads. Hand the goroutine its own deep
	// copy of fn — the caller's *fn is read-only today, but copying keeps this
	// detached read off the shared object (symmetric with the scalars captured above).
	fnReady := fn.DeepCopy()
	gp.runDetached(ctx, "set function ready", func(dctx context.Context) {
		executorUtil.SetFunctionReady(dctx, gp.logger, gp.fissionClient, fnReady, fv1.FunctionReasonReady, "function is serving via specialized pod "+podName)
	})
	return fsvc, nil
}

// detachedWriteTimeout bounds a best-effort post-specialization write started by
// runDetached. The original synchronous calls inherited the specialization
// deadline (~130s); the detached copies need their own so a hung apiserver under
// a cold-start storm cannot leak goroutines/connections without bound.
const detachedWriteTimeout = 30 * time.Second

// runDetached runs a best-effort write that must outlive the request that
// triggered it (the caller's RPC ctx is cancelled the moment getFuncSvc
// returns). The context is detached from cancellation but kept on a deadline
// (detachedWriteTimeout), and a recover guards the bare goroutine — unlike the
// previous synchronous calls there is no net/http handler recover here, so an
// unguarded panic would take down the whole executor.
func (gp *GenericPool) runDetached(ctx context.Context, op string, fn func(context.Context)) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				gp.logger.Error(fmt.Errorf("panic: %v", r), "recovered panic in detached operation", "op", op)
			}
		}()
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), detachedWriteTimeout)
		defer cancel()
		fn(dctx)
	}()
}

// destroys the pool -- the deployment, replicaset and pods
func (gp *GenericPool) destroy(ctx context.Context) error {
	gp.lock.Lock()
	defer gp.lock.Unlock()
	gp.readyPodQueue.ShutDown()
	if gp.cancelPoolCtx != nil {
		gp.cancelPoolCtx()
	}

	deletePropagation := metav1.DeletePropagationBackground
	delOpt := metav1.DeleteOptions{
		PropagationPolicy: &deletePropagation,
	}

	err := gp.kubernetesClient.AppsV1().
		Deployments(gp.fnNamespace).Delete(ctx, gp.deployment.Name, delOpt)
	if err != nil {
		if k8s_err.IsNotFound(err) {
			// Already gone (e.g. namespace teardown raced us) — destroy is
			// idempotent, nothing left to do.
			gp.logger.V(1).Info("deployment already deleted",
				"deployment_name", gp.deployment.Name,
				"deployment_namespace", gp.fnNamespace)
			return nil
		}
		gp.logger.Error(err, "error destroying deployment", "deployment_name", gp.deployment.Name,
			"deployment_namespace", gp.fnNamespace)
		return err
	}
	return nil
}
