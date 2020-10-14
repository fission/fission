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
	"net"
	"os"
	"strings"
	"time"

	"github.com/dchest/uniuri"
	"github.com/fission/fission/pkg/utils"
	multierror "github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	k8sErrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/executor/fscache"
	"github.com/fission/fission/pkg/executor/util"
	fetcherClient "github.com/fission/fission/pkg/fetcher/client"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
)

type (
	GenericPool struct {
		logger                 *zap.Logger
		env                    *fv1.Environment
		replicas               int32                         // num idle pods
		deployment             *appsv1.Deployment            // kubernetes deployment
		namespace              string                        // namespace to keep our resources
		functionNamespace      string                        // fallback namespace for fission functions
		podReadyTimeout        time.Duration                 // timeout for generic pods to become ready
		fsCache                *fscache.FunctionServiceCache // cache funcSvc's by function, address and podname
		useSvc                 bool                          // create k8s service for specialized pods
		useIstio               bool
		poolInstanceId         string           // small random string to uniquify pod names
		runtimeImagePullPolicy apiv1.PullPolicy // pull policy for generic pool to created env deployment
		kubernetesClient       *kubernetes.Clientset
		fissionClient          *crd.FissionClient
		instanceId             string // poolmgr instance id
		requestChannel         chan *choosePodRequest
		fetcherConfig          *fetcherConfig.Config
		stopCh                 context.CancelFunc
	}

	// serialize the choosing of pods so that choices don't conflict
	choosePodRequest struct {
		newLabels       map[string]string
		responseChannel chan *choosePodResponse
	}
	choosePodResponse struct {
		pod *apiv1.Pod
		error
	}
)

func MakeGenericPool(
	logger *zap.Logger,
	fissionClient *crd.FissionClient,
	kubernetesClient *kubernetes.Clientset,
	env *fv1.Environment,
	initialReplicas int32,
	namespace string,
	functionNamespace string,
	fsCache *fscache.FunctionServiceCache,
	fetcherConfig *fetcherConfig.Config,
	instanceId string,
	enableIstio bool) (*GenericPool, error) {

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

	gpLogger.Info("creating pool", zap.Any("environment", env.ObjectMeta))

	ctx, stopCh := context.WithCancel(context.Background())

	// TODO: in general we need to provide the user a way to configure pools.  Initial
	// replicas, autoscaling params, various timeouts, etc.
	gp := &GenericPool{
		logger:            gpLogger,
		env:               env,
		replicas:          initialReplicas, // TODO make this an env param instead?
		requestChannel:    make(chan *choosePodRequest),
		fissionClient:     fissionClient,
		kubernetesClient:  kubernetesClient,
		namespace:         namespace,
		functionNamespace: functionNamespace,
		podReadyTimeout:   podReadyTimeout,
		fsCache:           fsCache,
		poolInstanceId:    uniuri.NewLen(8),
		fetcherConfig:     fetcherConfig,
		instanceId:        instanceId,
		useSvc:            false,       // defaults off -- svc takes a second or more to become routable, slowing cold start
		useIstio:          enableIstio, // defaults off -- istio integration requires pod relabeling and it takes a second or more to become routable, slowing cold start
		stopCh:            stopCh,
	}

	gp.runtimeImagePullPolicy = utils.GetImagePullPolicy(os.Getenv("RUNTIME_IMAGE_PULL_POLICY"))

	// create fetcher SA in this ns, if not already created
	err = fetcherConfig.SetupServiceAccount(gp.kubernetesClient, gp.namespace, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "error creating fetcher service account in namespace %q", gp.namespace)
	}

	// Labels for generic deployment/RS/pods.
	//gp.labelsForPool = gp.getDeployLabels()

	// create the pool
	err = gp.createPool()
	if err != nil {
		return nil, err
	}
	gpLogger.Info("deployment created", zap.Any("environment", env.ObjectMeta))

	go gp.choosePodService(ctx)

	return gp, nil
}

func (gp *GenericPool) getEnvironmentPoolLabels() map[string]string {
	return map[string]string{
		fv1.EXECUTOR_TYPE:         string(fv1.ExecutorTypePoolmgr),
		fv1.ENVIRONMENT_NAME:      gp.env.ObjectMeta.Name,
		fv1.ENVIRONMENT_NAMESPACE: gp.env.ObjectMeta.Namespace,
		fv1.ENVIRONMENT_UID:       string(gp.env.ObjectMeta.UID),
		"managed":                 "true", // this allows us to easily find pods managed by the deployment
	}
}

func (gp *GenericPool) getDeployAnnotations() map[string]string {
	return map[string]string{
		fv1.EXECUTOR_INSTANCEID_LABEL: gp.instanceId,
	}
}

// choosePodService serializes the choosing of pods
func (gp *GenericPool) choosePodService(ctx context.Context) {
	for {
		select {
		case req := <-gp.requestChannel:
			pod, err := gp._choosePod(req.newLabels)
			if err != nil {
				req.responseChannel <- &choosePodResponse{error: err}
				continue
			}
			req.responseChannel <- &choosePodResponse{pod: pod}
		case <-ctx.Done():
			return
		}
	}
}

// choosePod picks a ready pod from the pool and relabels it, waiting if necessary.
// returns the pod API object.
func (gp *GenericPool) choosePod(newLabels map[string]string) (*apiv1.Pod, error) {
	req := &choosePodRequest{
		newLabels:       newLabels,
		responseChannel: make(chan *choosePodResponse),
	}
	gp.requestChannel <- req
	resp := <-req.responseChannel
	return resp.pod, resp.error
}

// _choosePod is called serially by choosePodService
func (gp *GenericPool) _choosePod(newLabels map[string]string) (*apiv1.Pod, error) {
	startTime := time.Now()
	for {
		// Retries took too long, error out.
		if time.Since(startTime) > gp.podReadyTimeout {
			gp.logger.Error("timed out waiting for pod", zap.Any("labels", newLabels), zap.Duration("timeout", gp.podReadyTimeout))
			return nil, errors.New("timeout: waited too long to get a ready pod")
		}

		// Get pods; filter the ones that are ready
		podList, err := gp.kubernetesClient.CoreV1().Pods(gp.namespace).List(
			metav1.ListOptions{
				FieldSelector: "status.phase=Running",
				LabelSelector: labels.Set(
					gp.deployment.Spec.Selector.MatchLabels).AsSelector().String(),
			})
		if err != nil {
			return nil, err
		}
		readyPods := make([]*apiv1.Pod, 0, len(podList.Items))
		for i := range podList.Items {
			pod := podList.Items[i]

			// Ignore not ready pod here
			if !utils.IsReadyPod(&pod) {
				continue
			}

			// add it to the list of ready pods
			readyPods = append(readyPods, &pod)
			break
		}
		gp.logger.Info("found ready pods",
			zap.Any("labels", newLabels),
			zap.Int("ready_count", len(readyPods)),
			zap.Int("total", len(podList.Items)))

		// If there are no ready pods, wait and retry.
		if len(readyPods) == 0 {
			err = gp.waitForReadyPod()
			if err != nil {
				return nil, err
			}
			continue
		}

		// Pick a ready pod.  For now just choose randomly;
		// ideally we'd care about which node it's running on,
		// and make a good scheduling decision.
		chosenPod := readyPods[0]

		if gp.env.Spec.AllowedFunctionsPerContainer != fv1.AllowedFunctionsPerContainerInfinite {
			// Relabel.  If the pod already got picked and
			// modified, this should fail; in that case just
			// retry.
			labelPatch, _ := json.Marshal(newLabels)

			// Append executor instance id to pod annotations to
			// indicate this pod is managed by this executor.
			annotations := gp.getDeployAnnotations()
			annotationPatch, _ := json.Marshal(annotations)

			patch := fmt.Sprintf(`{"metadata":{"annotations":%v, "labels":%v}}`, string(annotationPatch), string(labelPatch))
			gp.logger.Info("relabel pod", zap.String("pod", patch))
			newPod, err := gp.kubernetesClient.CoreV1().Pods(chosenPod.Namespace).Patch(chosenPod.Name, k8sTypes.StrategicMergePatchType, []byte(patch))
			if err != nil {
				gp.logger.Error("failed to relabel pod", zap.Error(err), zap.String("pod", chosenPod.Name))
				continue
			}

			// With StrategicMergePatchType, the client-go sometimes return
			// nil error and the labels & annotations remain the same.
			// So we have to check both of them to ensure the patch success.
			for k, v := range newLabels {
				if newPod.Labels[k] != v {
					return nil, errors.Errorf("value of necessary labels '%v' mismatch: want '%v', get '%v'",
						k, v, newPod.Labels[k])
				}
			}
			for k, v := range annotations {
				if newPod.Annotations[k] != v {
					return nil, errors.Errorf("value of necessary annotations '%v' mismatch: want '%v', get '%v'",
						k, v, newPod.Annotations[k])
				}
			}
		}

		gp.logger.Info("chose pod", zap.Any("labels", newLabels),
			zap.String("pod", chosenPod.Name), zap.Duration("elapsed_time", time.Since(startTime)))

		return chosenPod, nil
	}
}

func (gp *GenericPool) labelsForFunction(metadata *metav1.ObjectMeta) map[string]string {
	label := gp.getEnvironmentPoolLabels()
	label[fv1.FUNCTION_NAME] = metadata.Name
	label[fv1.FUNCTION_UID] = string(metadata.UID)
	label[fv1.FUNCTION_NAMESPACE] = metadata.Namespace // function CRD must stay within same namespace of environment CRD
	label["managed"] = "false"                         // this allows us to easily find pods not managed by the deployment
	return label
}

func (gp *GenericPool) scheduleDeletePod(name string) {
	go func() {
		// The sleep allows debugging or collecting logs from the pod before it's
		// cleaned up.  (We need a better solutions for both those things; log
		// aggregation and storage will help.)
		gp.logger.Error("error in pod - scheduling cleanup", zap.String("pod", name))
		gp.kubernetesClient.CoreV1().Pods(gp.namespace).Delete(name, nil)
	}()
}

func IsIPv6(podIP string) bool {
	ip := net.ParseIP(podIP)
	return ip != nil && strings.Contains(podIP, ":")
}

func (gp *GenericPool) getFetcherUrl(podIP string) string {
	testUrl := os.Getenv("TEST_FETCHER_URL")
	if len(testUrl) != 0 {
		// it takes a second or so for the test service to
		// become routable once a pod is relabeled. This is
		// super hacky, but only runs in unit tests.
		time.Sleep(5 * time.Second)
		return testUrl
	}
	isv6 := IsIPv6(podIP)
	var baseUrl string

	if isv6 { // We use bracket if the IP is in IPv6.
		baseUrl = fmt.Sprintf("http://[%v]:8000/", podIP)
	} else {
		baseUrl = fmt.Sprintf("http://%v:8000/", podIP)
	}

	return baseUrl

}

// specializePod chooses a pod, copies the required user-defined function to that pod
// (via fetcher), and calls the function-run container to load it, resulting in a
// specialized pod.
func (gp *GenericPool) specializePod(ctx context.Context, pod *apiv1.Pod, fn *fv1.Function) error {
	// for fetcher we don't need to create a service, just talk to the pod directly
	podIP := pod.Status.PodIP
	if len(podIP) == 0 {
		return errors.Errorf("Pod %s in namespace %s has no IP", pod.ObjectMeta.Name, pod.ObjectMeta.Namespace)
	}
	// specialize pod with service
	if gp.useIstio {
		svc := utils.GetFunctionIstioServiceName(fn.ObjectMeta.Name, fn.ObjectMeta.Namespace)
		podIP = fmt.Sprintf("%v.%v", svc, gp.namespace)
	}

	// tell fetcher to get the function.
	fetcherUrl := gp.getFetcherUrl(podIP)
	gp.logger.Info("calling fetcher to copy function", zap.String("function", fn.ObjectMeta.Name), zap.String("url", fetcherUrl))

	specializeReq := gp.fetcherConfig.NewSpecializeRequest(fn, gp.env)

	gp.logger.Info("specializing pod", zap.String("function", fn.ObjectMeta.Name))

	// Fetcher will download user function to share volume of pod, and
	// invoke environment specialize api for pod specialization.
	err := fetcherClient.MakeClient(gp.logger, fetcherUrl).Specialize(ctx, &specializeReq)
	if err != nil {
		return err
	}

	return nil
}

// getPoolName returns a unique name of an environment
func (gp *GenericPool) getPoolName() string {
	return strings.ToLower(fmt.Sprintf("poolmgr-%v-%v-%v", gp.env.ObjectMeta.Name, gp.env.ObjectMeta.Namespace, gp.env.ObjectMeta.ResourceVersion))
}

// A pool is a deployment of generic containers for an env.  This
// creates the pool but doesn't wait for any pods to be ready.
func (gp *GenericPool) createPool() error {
	deployLabels := gp.getEnvironmentPoolLabels()
	deployAnnotations := gp.getDeployAnnotations()

	// Use long terminationGracePeriodSeconds for connection draining in case that
	// pod still runs user functions.
	gracePeriodSeconds := int64(6 * 60)
	if gp.env.Spec.TerminationGracePeriod > 0 {
		gracePeriodSeconds = gp.env.Spec.TerminationGracePeriod
	}

	podAnnotations := gp.env.ObjectMeta.Annotations
	if podAnnotations == nil {
		podAnnotations = make(map[string]string)
	}

	// Here, we don't append executor instance-id to pod annotations
	// to prevent unwanted rolling updates occur. Pool manager will
	// append executor instance-id to pod annotations when a pod is chosen
	// for function specialization.

	if gp.useIstio && gp.env.Spec.AllowAccessToExternalNetwork {
		podAnnotations["sidecar.istio.io/inject"] = "false"
	}

	podLabels := gp.env.ObjectMeta.Labels
	if podLabels == nil {
		podLabels = make(map[string]string)
	}

	for k, v := range deployLabels {
		podLabels[k] = v
	}

	container, err := util.MergeContainer(&apiv1.Container{
		Name:                   gp.env.ObjectMeta.Name,
		Image:                  gp.env.Spec.Runtime.Image,
		ImagePullPolicy:        gp.runtimeImagePullPolicy,
		TerminationMessagePath: "/dev/termination-log",
		Resources:              gp.env.Spec.Resources,
		// Pod is removed from endpoints list for service when it's
		// state became "Termination". We used preStop hook as the
		// workaround for connection draining since pod maybe shutdown
		// before grace period expires.
		// https://kubernetes.io/docs/concepts/workloads/pods/pod/#termination-of-pods
		// https://github.com/kubernetes/kubernetes/issues/47576#issuecomment-308900172
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
	}, gp.env.Spec.Runtime.Container)
	if err != nil {
		return err
	}

	pod := apiv1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      podLabels,
			Annotations: podAnnotations,
		},
		Spec: apiv1.PodSpec{
			Containers:         []apiv1.Container{*container},
			ServiceAccountName: "fission-fetcher",
			// TerminationGracePeriodSeconds should be equal to the
			// sleep time of preStop to make sure that SIGTERM is sent
			// to pod after 6 mins.
			TerminationGracePeriodSeconds: &gracePeriodSeconds,
		},
	}

	pod.Spec = *(util.ApplyImagePullSecret(gp.env.Spec.ImagePullSecret, pod.Spec))

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        gp.getPoolName(),
			Labels:      deployLabels,
			Annotations: deployAnnotations,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &gp.replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: deployLabels,
			},
			Template: pod,
		},
	}

	// Order of merging is important here - first fetcher, then containers and lastly pod spec
	err = gp.fetcherConfig.AddFetcherToPodSpec(&deployment.Spec.Template.Spec, gp.env.ObjectMeta.Name)
	if err != nil {
		return err
	}

	if gp.env.Spec.Runtime.PodSpec != nil {
		newPodSpec, err := util.MergePodSpec(&deployment.Spec.Template.Spec, gp.env.Spec.Runtime.PodSpec)
		if err != nil {
			return err
		}
		deployment.Spec.Template.Spec = *newPodSpec
	}

	depl, err := gp.kubernetesClient.AppsV1().Deployments(gp.namespace).Get(deployment.Name, metav1.GetOptions{})
	if err == nil {
		if depl.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] != gp.instanceId {
			deployment.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] = gp.instanceId
			// Update with the latest deployment spec. Kubernetes will trigger
			// rolling update if spec is different from the one in the cluster.
			depl, err = gp.kubernetesClient.AppsV1().Deployments(gp.namespace).Update(deployment)
		}
		gp.deployment = depl
		return err
	} else if !k8sErrs.IsNotFound(err) {
		gp.logger.Error("error getting deployment in kubernetes", zap.Error(err), zap.String("deployment", deployment.Name))
		return err
	}

	depl, err = gp.kubernetesClient.AppsV1().Deployments(gp.namespace).Create(deployment)
	if err != nil {
		gp.logger.Error("error creating deployment in kubernetes", zap.Error(err), zap.String("deployment", deployment.Name))
		return err
	}

	gp.deployment = depl
	return nil
}

func (gp *GenericPool) waitForReadyPod() error {
	startTime := time.Now()
	for {
		// TODO: for now we just poll; use a watch instead
		depl, err := gp.kubernetesClient.AppsV1().Deployments(gp.namespace).Get(
			gp.deployment.ObjectMeta.Name, metav1.GetOptions{})
		if err != nil {
			e := "error waiting for ready pod for deployment"
			gp.logger.Error(e, zap.String("deployment", gp.deployment.ObjectMeta.Name), zap.String("namespace", gp.namespace))
			return fmt.Errorf("%s %q in namespace %q", e, gp.deployment.ObjectMeta.Name, gp.namespace)
		}

		gp.deployment = depl
		if gp.deployment.Status.AvailableReplicas > 0 {
			return nil
		}

		if time.Since(startTime) > gp.podReadyTimeout {
			podList, err := gp.kubernetesClient.CoreV1().Pods(gp.namespace).List(metav1.ListOptions{
				LabelSelector: labels.Set(
					gp.deployment.Spec.Selector.MatchLabels).AsSelector().String(),
			})
			if err != nil {
				gp.logger.Error("error getting pod list after timeout waiting for ready pod", zap.Error(err))
			}

			// Since even single pod is not ready, choosing the first pod to inspect is a good approximation. In future this can be done better
			pod := podList.Items[0]
			errs := &multierror.Error{}
			for _, cStatus := range pod.Status.ContainerStatuses {
				if !cStatus.Ready {
					errs = multierror.Append(errs, errors.New(fmt.Sprintf("%v: %v", cStatus.State.Waiting.Reason, cStatus.State.Waiting.Message)))
				}
			}
			if errs.ErrorOrNil() != nil {
				return errors.Wrapf(errs, "Timeout: waited too long for pod of deployment %v in namespace %v to be ready",
					gp.deployment.ObjectMeta.Name, gp.namespace)
			}
			return nil
		}
		time.Sleep(1000 * time.Millisecond)
	}
}

func (gp *GenericPool) createSvc(name string, labels map[string]string) (*apiv1.Service, error) {
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
	svc, err := gp.kubernetesClient.CoreV1().Services(gp.namespace).Create(&service)
	return svc, err
}

func (gp *GenericPool) getFuncSvc(ctx context.Context, fn *fv1.Function) (*fscache.FuncSvc, error) {
	gp.logger.Info("choosing pod from pool", zap.Any("function", fn.ObjectMeta))
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
		podList, err := gp.kubernetesClient.CoreV1().Pods(gp.namespace).List(metav1.ListOptions{
			LabelSelector: labels.Set(sel).AsSelector().String(),
		})
		if err != nil {
			return nil, err
		}

		// Remove old versions function pods
		for _, pod := range podList.Items {
			// Delete pod no matter what status it is
			gp.kubernetesClient.CoreV1().Pods(gp.namespace).Delete(pod.ObjectMeta.Name, nil)
		}
	}

	pod, err := gp.choosePod(funcLabels)
	if err != nil {
		return nil, err
	}

	err = gp.specializePod(ctx, pod, fn)
	if err != nil {
		gp.scheduleDeletePod(pod.ObjectMeta.Name)
		return nil, err
	}
	gp.logger.Info("specialized pod", zap.String("pod", pod.ObjectMeta.Name), zap.Any("function", fn.ObjectMeta))

	var svcHost string
	if gp.useSvc && !gp.useIstio {
		svcName := fmt.Sprintf("svc-%v", fn.ObjectMeta.Name)
		if len(fn.ObjectMeta.UID) > 0 {
			svcName = fmt.Sprintf("%s-%v", svcName, fn.ObjectMeta.UID)
		}

		svc, err := gp.createSvc(svcName, funcLabels)
		if err != nil {
			gp.scheduleDeletePod(pod.ObjectMeta.Name)
			return nil, err
		}
		if svc.ObjectMeta.Name != svcName {
			gp.scheduleDeletePod(pod.ObjectMeta.Name)
			return nil, errors.Errorf("sanity check failed for svc %v", svc.ObjectMeta.Name)
		}

		// the fission router isn't in the same namespace, so return a
		// namespace-qualified hostname
		svcHost = fmt.Sprintf("%v.%v:8888", svcName, gp.namespace)
	} else if gp.useIstio {
		svc := utils.GetFunctionIstioServiceName(fn.ObjectMeta.Name, fn.ObjectMeta.Namespace)
		svcHost = fmt.Sprintf("%v.%v:8888", svc, gp.namespace)
	} else {
		svcHost = fmt.Sprintf("%v:8888", pod.Status.PodIP)
	}

	// patch svc-host and resource version to the pod annotations for new executor to adopt the pod
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"%v":"%v","%v":"%v"}}}`,
		fv1.ANNOTATION_SVC_HOST, svcHost, fv1.FUNCTION_RESOURCE_VERSION, fn.ObjectMeta.ResourceVersion)
	p, err := gp.kubernetesClient.CoreV1().Pods(pod.Namespace).Patch(pod.Name, k8sTypes.StrategicMergePatchType, []byte(patch))
	if err != nil {
		// just log the error since it won't affect the function serving
		gp.logger.Warn("error patching svc-host to pod", zap.Error(err),
			zap.String("pod", pod.Name), zap.String("ns", pod.Namespace))
	} else {
		pod = p
	}

	gp.logger.Info("specialized pod",
		zap.String("pod", pod.ObjectMeta.Name),
		zap.String("podNamespace", pod.ObjectMeta.Namespace),
		zap.String("function", fn.ObjectMeta.Name),
		zap.String("functionNamespace", fn.ObjectMeta.Namespace),
		zap.String("specialization_host", svcHost))

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

	m := fn.ObjectMeta // only cache necessary part
	fsvc := &fscache.FuncSvc{
		Name:              pod.ObjectMeta.Name,
		Function:          &m,
		Environment:       gp.env,
		Address:           svcHost,
		KubernetesObjects: kubeObjRefs,
		Executor:          fv1.ExecutorTypePoolmgr,
		Ctime:             time.Now(),
		Atime:             time.Now(),
	}

	gp.fsCache.AddFunc(*fsvc)

	gp.fsCache.IncreaseColdStarts(fn.ObjectMeta.Name, string(fn.ObjectMeta.UID))

	return fsvc, nil
}

// destroys the pool -- the deployment, replicaset and pods
func (gp *GenericPool) destroy() error {
	gp.stopCh()

	deletePropagation := metav1.DeletePropagationBackground
	delOpt := metav1.DeleteOptions{
		PropagationPolicy: &deletePropagation,
	}

	err := gp.kubernetesClient.AppsV1().
		Deployments(gp.namespace).Delete(gp.deployment.ObjectMeta.Name, &delOpt)
	if err != nil {
		gp.logger.Error("error destroying deployment",
			zap.Error(err),
			zap.String("deployment_name", gp.deployment.ObjectMeta.Name),
			zap.String("deployment_namespace", gp.namespace))
		return err
	}
	return nil
}
