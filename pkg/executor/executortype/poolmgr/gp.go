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
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/dchest/uniuri"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	k8s_err "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/fscache"
	fetcherClient "github.com/fission/fission/pkg/fetcher/client"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

type (
	// GenericPool represents a generic environment pool
	GenericPool struct {
		logger                   *zap.Logger
		env                      *fv1.Environment
		deployment               *appsv1.Deployment            // kubernetes deployment
		fnNamespace              string                        // namespace to keep our resources
		podReadyTimeout          time.Duration                 // timeout for generic pods to become ready
		fsCache                  *fscache.FunctionServiceCache // cache funcSvc's by function, address and podname
		useSvc                   bool                          // create k8s service for specialized pods
		useIstio                 bool
		runtimeImagePullPolicy   apiv1.PullPolicy // pull policy for generic pool to created env deployment
		kubernetesClient         kubernetes.Interface
		metricsClient            metricsclient.Interface
		fissionClient            versioned.Interface
		fetcherConfig            *fetcherConfig.Config
		stopReadyPodControllerCh chan struct{}
		readyPodLister           corelisters.PodLister
		readyPodListerSynced     cache.InformerSynced
		readyPodQueue            workqueue.DelayingInterface
		poolInstanceID           string // small random string to uniquify pod names
		instanceID               string // poolmgr instance id
		podSpecPatch             *apiv1.PodSpec
		// TODO: move this field into fsCache
		podFSVCMap sync.Map
	}
)

// MakeGenericPool returns an instance of GenericPool
func MakeGenericPool(
	logger *zap.Logger,
	fissionClient versioned.Interface,
	kubernetesClient kubernetes.Interface,
	metricsClient metricsclient.Interface,
	env *fv1.Environment,
	fnNamespace string,
	fsCache *fscache.FunctionServiceCache,
	fetcherConfig *fetcherConfig.Config,
	instanceID string,
	enableIstio bool,
	podSpecPatch *apiv1.PodSpec) *GenericPool {

	gpLogger := logger.Named("generic_pool")

	podReadyTimeoutStr := os.Getenv("POD_READY_TIMEOUT")
	podReadyTimeout, err := time.ParseDuration(podReadyTimeoutStr)
	if err != nil {
		podReadyTimeout = 300 * time.Second
		gpLogger.Error("failed to parse pod ready timeout duration from 'POD_READY_TIMEOUT' - set to the default value",
			zap.Error(err),
			zap.String("value", podReadyTimeoutStr),
			zap.Duration("default", podReadyTimeout))
	}

	gpLogger.Info("creating pool", zap.Any("environment", env))

	// TODO: in general we need to provide the user a way to configure pools.  Initial
	// replicas, autoscaling params, various timeouts, etc.
	gp := &GenericPool{
		logger:                   gpLogger,
		env:                      env,
		fissionClient:            fissionClient,
		kubernetesClient:         kubernetesClient,
		metricsClient:            metricsClient,
		fnNamespace:              fnNamespace,
		podReadyTimeout:          podReadyTimeout,
		fsCache:                  fsCache,
		fetcherConfig:            fetcherConfig,
		useSvc:                   false,       // defaults off -- svc takes a second or more to become routable, slowing cold start
		useIstio:                 enableIstio, // defaults off -- istio integration requires pod relabeling and it takes a second or more to become routable, slowing cold start
		stopReadyPodControllerCh: make(chan struct{}),
		poolInstanceID:           uniuri.NewLen(8),
		instanceID:               instanceID,
		podFSVCMap:               sync.Map{},
		podSpecPatch:             podSpecPatch,
	}

	gp.runtimeImagePullPolicy = utils.GetImagePullPolicy(os.Getenv("RUNTIME_IMAGE_PULL_POLICY"))

	return gp
}

func (gp *GenericPool) setup(ctx context.Context) error {
	// create the pool
	err := gp.createPoolDeployment(ctx, gp.env)
	if err != nil {
		return err
	}
	err = gp.setupReadyPodController()
	if err != nil {
		return err
	}
	go gp.updateCPUUtilizationSvc(ctx)
	return nil
}

func (gp *GenericPool) getEnvironmentPoolLabels(env *fv1.Environment) map[string]string {
	envLabels := maps.Clone(env.ObjectMeta.Labels)
	envLabels[fv1.EXECUTOR_TYPE] = string(fv1.ExecutorTypePoolmgr)
	envLabels[fv1.ENVIRONMENT_NAME] = env.ObjectMeta.Name
	envLabels[fv1.ENVIRONMENT_NAMESPACE] = env.ObjectMeta.Namespace
	envLabels[fv1.ENVIRONMENT_UID] = string(env.ObjectMeta.UID)
	envLabels["managed"] = "true" // this allows us to easily find pods managed by the deployment
	return envLabels
}

func (gp *GenericPool) getDeployAnnotations(env *fv1.Environment) map[string]string {
	deployAnnotations := maps.Clone(env.Annotations)
	deployAnnotations[fv1.EXECUTOR_INSTANCEID_LABEL] = gp.instanceID
	return deployAnnotations
}

func (gp *GenericPool) checkMetricsApi() bool {
	apiGroups, err := gp.metricsClient.Discovery().ServerGroups()
	if err != nil {
		gp.logger.Error("failed to discover API groups", zap.Error(err))
		return false
	}
	return utils.SupportedMetricsAPIVersionAvailable(apiGroups)
}

func (gp *GenericPool) updateCPUUtilizationSvc(ctx context.Context) {
	var metricsApiAvailabe bool
	checkDuration := 30

	if !gp.checkMetricsApi() {
		checkDuration = 180
		gp.logger.Warn("Metrics API not available")
	}

	serviceFunc := func(ctx context.Context) {
		podMetricsList, err := gp.metricsClient.MetricsV1beta1().PodMetricses(gp.fnNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "managed=false",
		})
		if err != nil {
			gp.logger.Error("failed to fetch pod metrics list", zap.Error(err))
			return
		}
		gp.logger.Debug("pods found", zap.Any("length", len(podMetricsList.Items)))
		for _, val := range podMetricsList.Items {
			p, _ := resource.ParseQuantity("0m")
			for _, container := range val.Containers {
				p.Add(container.Usage["cpu"])
			}
			if value, ok := gp.podFSVCMap.Load(val.ObjectMeta.Name); ok {
				if valArray, ok1 := value.([]interface{}); ok1 {
					function, address := valArray[0], valArray[1]
					gp.fsCache.SetCPUUtilizaton(function.(crd.CacheKeyURG), address.(string), p)
					gp.logger.Info(fmt.Sprintf("updated function %s, address %s, cpuUsage %+v", function.(string), address.(string), p))
				}
			}
		}
	}

	for {
		if metricsApiAvailabe {
			serviceFunc(ctx)
		} else {
			if gp.checkMetricsApi() {
				metricsApiAvailabe = true
				checkDuration = 30
			}
		}
		time.Sleep(time.Duration(checkDuration) * time.Second)
	}
}

// choosePod picks a ready pod from the pool and relabels it, waiting if necessary.
// returns the key and pod API object.
func (gp *GenericPool) choosePod(ctx context.Context, newLabels map[string]string) (string, *apiv1.Pod, error) {
	startTime := time.Now()
	podTimeout := startTime.Add(gp.podReadyTimeout)
	deadline, ok := ctx.Deadline()
	if ok {
		deadline = deadline.Add(-1 * time.Second)
		if deadline.Before(podTimeout) {
			podTimeout = deadline
		}
	}
	expoDelay := 100 * time.Millisecond
	logger := otelUtils.LoggerWithTraceID(ctx, gp.logger)
	if !cache.WaitForCacheSync(ctx.Done(), gp.readyPodListerSynced) {
		logger.Error("timed out waiting for ready pod lister synced")
		return "", nil, errors.New("ready pod lister not synced")
	}
	for {
		// Retries took too long, error out.
		if time.Now().After(podTimeout) {
			logger.Error("timed out waiting for pod", zap.Any("labels", newLabels), zap.Duration("timeout", podTimeout.Sub(startTime)))
			return "", nil, errors.New("timeout: waited too long to get a ready pod")
		}
		if ctx.Err() != nil {
			logger.Error("context canceled while waiting for pod", zap.Any("labels", newLabels), zap.Duration("timeout", podTimeout.Sub(startTime)))
			return "", nil, fmt.Errorf("context canceled while waiting for pod: %w", ctx.Err())
		}

		var chosenPod *apiv1.Pod
		var key string

		otelUtils.SpanTrackEvent(ctx, "waitForPod", otelUtils.MapToAttributes(newLabels)...)
		item, quit := gp.readyPodQueue.Get()
		if quit {
			logger.Error("readypod controller is not running")
			return "", nil, errors.New("readypod controller is not running")
		}
		key = item.(string)
		logger.Debug("got key from the queue", zap.String("key", key))
		namespace, name, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			logger.Error("error splitting key", zap.Error(err), zap.String("key", key))
			gp.readyPodQueue.Done(key)
			return "", nil, err
		}
		pod, err := gp.readyPodLister.Pods(namespace).Get(name)
		if err != nil {
			logger.Error("fetching object from store failed", zap.String("key", key), zap.Error(err))
			gp.readyPodQueue.Done(key)
			continue
		}
		if utils.IsPodTerminated(pod) {
			logger.Error("pod is terminated", zap.String("key", key))
			gp.readyPodQueue.Done(key)
			continue
		}
		if !utils.IsReadyPod(pod) {
			logger.Warn("pod not ready, pod will be checked again", zap.String("key", key), zap.Duration("delay", expoDelay))
			gp.readyPodQueue.Done(key)
			gp.readyPodQueue.AddAfter(key, expoDelay)
			expoDelay *= 2
			continue
		}
		chosenPod = pod.DeepCopy()
		otelUtils.SpanTrackEvent(ctx, "foundPod", otelUtils.GetAttributesForPod(chosenPod)...)

		if gp.env.Spec.AllowedFunctionsPerContainer != fv1.AllowedFunctionsPerContainerInfinite {
			// Relabel.  If the pod already got picked and
			// modified, this should fail; in that case just
			// retry.
			labelPatch, _ := json.Marshal(newLabels)

			// Append executor instance id to pod annotations to
			// indicate this pod is managed by this executor.
			annotations := gp.getDeployAnnotations(gp.env)
			annotationPatch, _ := json.Marshal(annotations)

			patch := fmt.Sprintf(`{"metadata":{"annotations":%v, "labels":%v}}`, string(annotationPatch), string(labelPatch))
			logger.Info("relabel pod", zap.String("pod", patch))
			newPod, err := gp.kubernetesClient.CoreV1().Pods(chosenPod.Namespace).Patch(ctx, chosenPod.Name, k8sTypes.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
			if err != nil && errors.Is(err, context.Canceled) {
				// ending retry loop when the request canceled
				gp.readyPodQueue.Done(key)
				gp.readyPodQueue.AddAfter(key, expoDelay)
				return "", nil, errors.Errorf("failed to relabel pod: %s", err)
			} else if err != nil {
				logger.Error("failed to relabel pod", zap.Error(err), zap.String("pod", chosenPod.Name), zap.Duration("delay", expoDelay))
				gp.readyPodQueue.Done(key)
				gp.readyPodQueue.AddAfter(key, expoDelay)
				expoDelay *= 2
				continue
			}
			otelUtils.SpanTrackEvent(ctx, "podRelabel", otelUtils.GetAttributesForPod(chosenPod)...)

			// With StrategicMergePatchType, the client-go sometimes return
			// nil error and the labels & annotations remain the same.
			// So we have to check both of them to ensure the patch success.
			for k, v := range newLabels {
				if newPod.Labels[k] != v {
					return "", nil, errors.Errorf("value of necessary labels '%v' mismatch: want '%v', get '%v'",
						k, v, newPod.Labels[k])
				}
			}
			for k, v := range annotations {
				if newPod.Annotations[k] != v {
					return "", nil, errors.Errorf("value of necessary annotations '%v' mismatch: want '%v', get '%v'",
						k, v, newPod.Annotations[k])
				}
			}
		}

		logger.Info("chose pod", zap.Any("labels", newLabels),
			zap.String("pod", chosenPod.Name), zap.Duration("elapsed_time", time.Since(startTime)))

		return key, chosenPod, nil
	}
}

func (gp *GenericPool) labelsForFunction(metadata *metav1.ObjectMeta) map[string]string {
	label := gp.getEnvironmentPoolLabels(gp.env)
	label[fv1.FUNCTION_NAME] = metadata.Name
	label[fv1.FUNCTION_UID] = string(metadata.UID)
	label[fv1.FUNCTION_NAMESPACE] = metadata.Namespace // function CRD must stay within same namespace of environment CRD
	label["managed"] = "false"                         // this allows us to easily find pods not managed by the deployment
	return label
}

func (gp *GenericPool) scheduleDeletePod(ctx context.Context, name string) {
	// The sleep allows debugging or collecting logs from the pod before it's
	// cleaned up.  (We need a better solutions for both those things; log
	// aggregation and storage will help.)
	gp.logger.Error("error in pod - scheduling cleanup", zap.String("pod", name))
	err := gp.kubernetesClient.CoreV1().Pods(gp.fnNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		gp.logger.Error(
			"error deleting pod",
			zap.String("name", name),
			zap.String("namespace", gp.fnNamespace),
			zap.Error(err),
		)
	}
}

// IsIPv6 validates if the podIP follows to IPv6 protocol
func IsIPv6(podIP string) bool {
	ip := net.ParseIP(podIP)
	return ip != nil && strings.Contains(podIP, ":")
}

func (gp *GenericPool) getFetcherURL(podIP string) string {
	testURL := os.Getenv("TEST_FETCHER_URL")
	if len(testURL) != 0 {
		// it takes a second or so for the test service to
		// become routable once a pod is relabeled. This is
		// super hacky, but only runs in unit tests.
		time.Sleep(5 * time.Second)
		return testURL
	}

	isv6 := IsIPv6(podIP)
	var baseURL string

	if isv6 { // We use bracket if the IP is in IPv6.
		baseURL = fmt.Sprintf("http://[%v]:8000/", podIP)
	} else {
		baseURL = fmt.Sprintf("http://%v:8000/", podIP)
	}
	return baseURL
}

// specializePod chooses a pod, copies the required user-defined function to that pod
// (via fetcher), and calls the function-run container to load it, resulting in a
// specialized pod.
func (gp *GenericPool) specializePod(ctx context.Context, pod *apiv1.Pod, fn *fv1.Function) error {
	logger := otelUtils.LoggerWithTraceID(ctx, gp.logger)

	// for fetcher we don't need to create a service, just talk to the pod directly
	podIP := pod.Status.PodIP
	if len(podIP) == 0 {
		return errors.Errorf("Pod %s in namespace %s has no IP", pod.ObjectMeta.Name, pod.ObjectMeta.Namespace)
	}
	for _, cm := range fn.Spec.ConfigMaps {
		_, err := gp.kubernetesClient.CoreV1().ConfigMaps(gp.fnNamespace).Get(ctx, cm.Name, metav1.GetOptions{})
		if err != nil {
			if k8s_err.IsNotFound(err) {
				logger.Error("configmap namespace mismatch", zap.String("error", "configmap must be in same namespace as function namespace"),
					zap.String("configmap_name", cm.Name),
					zap.String("configmap_namespace", cm.Namespace),
					zap.String("function_name", fn.ObjectMeta.Name),
					zap.String("function_namespace", gp.fnNamespace))
				return fmt.Errorf(fmt.Sprintf("configmap %s must be in same namespace as function namespace", cm.Name))
			} else {
				return err
			}
		}
	}
	for _, sec := range fn.Spec.Secrets {
		_, err := gp.kubernetesClient.CoreV1().Secrets(gp.fnNamespace).Get(ctx, sec.Name, metav1.GetOptions{})
		if err != nil {
			if k8s_err.IsNotFound(err) {
				logger.Error("secret namespace mismatch", zap.String("error", "secret must be in same namespace as function namespace"),
					zap.String("secret_name", sec.Name),
					zap.String("secret_namespace", sec.Namespace),
					zap.String("function_name", fn.ObjectMeta.Name),
					zap.String("function_namespace", gp.fnNamespace))
				return fmt.Errorf(fmt.Sprintf("secret %s must be in same namespace as function namespace", sec.Name))
			} else {
				return err
			}

		}
	}
	// specialize pod with service
	if gp.useIstio {
		svc := utils.GetFunctionIstioServiceName(fn.ObjectMeta.Name, fn.ObjectMeta.Namespace)
		podIP = fmt.Sprintf("%v.%v", svc, gp.fnNamespace)
	}

	// tell fetcher to get the function.
	fetcherURL := gp.getFetcherURL(podIP)
	logger.Info("calling fetcher to copy function", zap.String("function", fn.ObjectMeta.Name), zap.String("url", fetcherURL))

	specializeReq := gp.fetcherConfig.NewSpecializeRequest(fn, gp.env)

	logger.Info("specializing pod", zap.String("function", fn.ObjectMeta.Name))

	// Fetcher will download user function to share volume of pod, and
	// invoke environment specialize api for pod specialization.
	err := fetcherClient.MakeClient(gp.logger, fetcherURL).Specialize(ctx, &specializeReq)
	if err != nil {
		return err
	}
	otelUtils.SpanTrackEvent(ctx, "specializedPod", otelUtils.GetAttributesForPod(pod)...)
	return nil
}

func (gp *GenericPool) createSvc(ctx context.Context, name string, labels map[string]string) (*apiv1.Service, error) {
	otelUtils.SpanTrackEvent(ctx, "createSvc", otelUtils.MapToAttributes(map[string]string{
		"name": name,
	})...)
	service := apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: apiv1.ServiceSpec{
			Type: apiv1.ServiceTypeClusterIP,
			Ports: []apiv1.ServicePort{
				{
					Protocol:   apiv1.ProtocolTCP,
					Port:       8888,
					TargetPort: intstr.FromInt(8888),
				},
			},
			Selector: labels,
		},
	}
	svc, err := gp.kubernetesClient.CoreV1().Services(gp.fnNamespace).Create(ctx, &service, metav1.CreateOptions{})
	return svc, err
}

func (gp *GenericPool) getFuncSvc(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	logger := otelUtils.LoggerWithTraceID(ctx, gp.logger).With(zap.String("function", fn.ObjectMeta.Name), zap.String("namespace", fn.ObjectMeta.Namespace),
		zap.String("env", fn.Spec.Environment.Name), zap.String("envNamespace", fn.Spec.Environment.Namespace))

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
			"functionName": fn.ObjectMeta.Name,
			"functionUid":  string(fn.ObjectMeta.UID),
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

	key, pod, err := gp.choosePod(ctx, funcLabels)
	if err != nil {
		return nil, err
	}
	gp.readyPodQueue.Done(key)
	err = gp.specializePod(ctx, pod, fn)
	if err != nil {
		go gp.scheduleDeletePod(context.Background(), pod.ObjectMeta.Name)
		return nil, err
	}
	logger.Info("specialized pod", zap.String("pod", pod.ObjectMeta.Name), zap.String("podNamespace", pod.ObjectMeta.Namespace), zap.String("podIP", pod.Status.PodIP))

	var svcHost string
	if gp.useSvc && !gp.useIstio {
		svcName := fmt.Sprintf("svc-%v", fn.ObjectMeta.Name)
		if len(fn.ObjectMeta.UID) > 0 {
			svcName = fmt.Sprintf("%s-%v", svcName, fn.ObjectMeta.UID)
		}

		svc, err := gp.createSvc(ctx, svcName, funcLabels)
		if err != nil {
			go gp.scheduleDeletePod(context.Background(), pod.ObjectMeta.Name)
			return nil, err
		}
		if svc.ObjectMeta.Name != svcName {
			go gp.scheduleDeletePod(context.Background(), pod.ObjectMeta.Name)
			return nil, errors.Errorf("sanity check failed for svc %v", svc.ObjectMeta.Name)
		}

		// the fission router isn't in the same namespace, so return a
		// namespace-qualified hostname
		svcHost = fmt.Sprintf("%v.%v:8888", svcName, gp.fnNamespace)
	} else if gp.useIstio {
		svc := utils.GetFunctionIstioServiceName(fn.ObjectMeta.Name, fn.ObjectMeta.Namespace)
		svcHost = fmt.Sprintf("%v.%v:8888", svc, gp.fnNamespace)
	} else {
		svcHost = fmt.Sprintf("%v:8888", pod.Status.PodIP)
	}

	otelUtils.SpanTrackEvent(ctx, "addFunctionLabel", otelUtils.GetAttributesForPod(pod)...)
	// patch svc-host and resource version to the pod annotations for new executor to adopt the pod
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"%v":"%v","%v":"%v"}}}`,
		fv1.ANNOTATION_SVC_HOST, svcHost, fv1.FUNCTION_RESOURCE_VERSION, fn.ObjectMeta.ResourceVersion)
	p, err := gp.kubernetesClient.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, k8sTypes.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		// just log the error since it won't affect the function serving
		logger.Warn("error patching svc-host to pod", zap.Error(err),
			zap.String("pod", pod.Name), zap.String("ns", pod.Namespace))
	} else {
		pod = p
	}

	kubeObjRefs := []apiv1.ObjectReference{
		{
			Kind:            "pod",
			Name:            pod.ObjectMeta.Name,
			APIVersion:      pod.TypeMeta.APIVersion,
			Namespace:       pod.ObjectMeta.Namespace,
			ResourceVersion: pod.ObjectMeta.ResourceVersion,
			UID:             pod.ObjectMeta.UID,
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
		logger.Error("failed to get 85 of CPU usage", zap.Error(err))
		cpuLimit = cpuUsage
	}
	logger.Debug("cpuLimit set to", zap.Any("cpulimit", cpuLimit))

	m := fn.ObjectMeta // only cache necessary part
	fsvc := &fscache.FuncSvc{
		Name:              pod.ObjectMeta.Name,
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
	gp.podFSVCMap.Store(pod.ObjectMeta.Name, []interface{}{crd.CacheKeyURGFromMeta(fsvc.Function), fsvc.Address})
	gp.fsCache.AddFunc(ctx, *fsvc, fn.GetRequestPerPod(), fn.GetRetainPods())

	logger.Info("added function service",
		zap.String("pod", pod.ObjectMeta.Name),
		zap.String("podNamespace", pod.ObjectMeta.Namespace),
		zap.String("serviceHost", svcHost),
		zap.String("podIP", pod.Status.PodIP))

	otelUtils.SpanTrackEvent(ctx, "getFuncSvcComplete", fscache.GetAttributesForFuncSvc(fsvc)...)
	return fsvc, nil
}

// getPercent returns  x percent of the quantity i.e multiple it x/100
func (gp *GenericPool) getPercent(cpuUsage resource.Quantity, percentage float64) (resource.Quantity, error) {
	val := int64(math.Ceil(float64(cpuUsage.MilliValue()) * percentage))
	return resource.ParseQuantity(fmt.Sprintf("%dm", val))
}

// destroys the pool -- the deployment, replicaset and pods
func (gp *GenericPool) destroy(ctx context.Context) error {
	close(gp.stopReadyPodControllerCh)

	deletePropagation := metav1.DeletePropagationBackground
	delOpt := metav1.DeleteOptions{
		PropagationPolicy: &deletePropagation,
	}

	err := gp.kubernetesClient.AppsV1().
		Deployments(gp.fnNamespace).Delete(ctx, gp.deployment.ObjectMeta.Name, delOpt)
	if err != nil {
		gp.logger.Error("error destroying deployment",
			zap.Error(err),
			zap.String("deployment_name", gp.deployment.ObjectMeta.Name),
			zap.String("deployment_namespace", gp.fnNamespace))
		return err
	}
	return nil
}
