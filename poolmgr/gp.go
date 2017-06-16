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
	"bytes"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dchest/uniuri"
	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/pkg/api"
	"k8s.io/client-go/1.5/pkg/api/v1"
	autoscalingv1 "k8s.io/client-go/1.5/pkg/apis/autoscaling/v1"
	"k8s.io/client-go/1.5/pkg/apis/extensions/v1beta1"
	"k8s.io/client-go/1.5/pkg/labels"
	"k8s.io/client-go/1.5/pkg/util/intstr"

	"github.com/fission/fission"
)

const POOLMGR_INSTANCEID_LABEL string = "poolmgrInstanceId"
const POD_PHASE_RUNNING string = "Running"

type (
	GenericPool struct {
		env              *fission.Environment
		replicas         int32               // num idle pods
		deployment       *v1beta1.Deployment // kubernetes deployment
		namespace        string              // namespace to keep our resources
		podReadyTimeout  time.Duration       // timeout for generic pods to become ready
		controllerUrl    string
		idlePodReapTime  time.Duration         // pods unused for idlePodReapTime are deleted
		fsCache          *functionServiceCache // cache funcSvc's by function, address and podname
		poolInstanceId   string                // small random string to uniquify pod names
		kubernetesClient *kubernetes.Clientset
		instanceId       string // poolmgr instance id
		labelsForPool    map[string]string
		requestChannel   chan *choosePodRequest
	}

	// serialize the choosing of pods so that choices don't conflict
	choosePodRequest struct {
		newLabels       map[string]string
		responseChannel chan *choosePodResponse
	}
	choosePodResponse struct {
		pod *v1.Pod
		error
	}
)

func MakeGenericPool(
	controllerUrl string,
	kubernetesClient *kubernetes.Clientset,
	env *fission.Environment,
	initialReplicas int32,
	namespace string,
	fsCache *functionServiceCache,
	instanceId string) (*GenericPool, error) {

	log.Printf("Creating pool for environment %v", env.Metadata)
	// TODO: in general we need to provide the user a way to configure pools.  Initial
	// replicas, autoscaling params, various timeouts, etc.
	gp := &GenericPool{
		env:              env,
		replicas:         initialReplicas, // TODO make this an env param instead?
		requestChannel:   make(chan *choosePodRequest),
		kubernetesClient: kubernetesClient,
		namespace:        namespace,
		podReadyTimeout:  5 * time.Minute, // TODO make this an env param?
		controllerUrl:    controllerUrl,
		idlePodReapTime:  3 * time.Minute, // TODO make this configurable
		fsCache:          fsCache,
		poolInstanceId:   uniuri.NewLen(8),
		instanceId:       instanceId,
	}

	// Labels for generic deployment/RS/pods.
	gp.labelsForPool = map[string]string{
		"environmentName":        gp.env.Metadata.Name,
		"environmentUid":         gp.env.Metadata.Uid,
		POOLMGR_INSTANCEID_LABEL: gp.instanceId,
	}

	// create the pool
	err := gp.createPool()
	if err != nil {
		return nil, err
	}
	log.Printf("[%v] Deployment created", env.Metadata)

	go gp.choosePodService()

	go gp.idlePodReaper()

	return gp, nil
}

// choosePodService serializes the choosing of pods
func (gp *GenericPool) choosePodService() {
	for {
		select {
		case req := <-gp.requestChannel:
			pod, err := gp._choosePod(req.newLabels)
			if err != nil {
				req.responseChannel <- &choosePodResponse{error: err}
				continue
			}
			req.responseChannel <- &choosePodResponse{pod: pod}
		}
	}
}

// choosePod picks a ready pod from the pool and relabels it, waiting if necessary.
// returns the pod API object.
func (gp *GenericPool) choosePod(newLabels map[string]string) (*v1.Pod, error) {
	req := &choosePodRequest{
		newLabels:       newLabels,
		responseChannel: make(chan *choosePodResponse),
	}
	gp.requestChannel <- req
	resp := <-req.responseChannel
	return resp.pod, resp.error
}

// _choosePod is called serially by choosePodService
func (gp *GenericPool) _choosePod(newLabels map[string]string) (*v1.Pod, error) {
	startTime := time.Now()
	for {
		// Retries took too long, error out.
		if time.Now().Sub(startTime) > gp.podReadyTimeout {
			log.Printf("[%v] Erroring out, timed out", newLabels)
			return nil, errors.New("timeout: waited too long to get a ready pod")
		}

		// Get pods; filter the ones that are ready
		podList, err := gp.kubernetesClient.Core().Pods(gp.namespace).List(
			api.ListOptions{
				LabelSelector: labels.Set(
					gp.deployment.Spec.Selector.MatchLabels).AsSelector(),
			})
		if err != nil {
			return nil, err
		}
		readyPods := make([]*v1.Pod, 0, len(podList.Items))
		for i := range podList.Items {
			pod := podList.Items[i]

			// If a pod has no IP it's not ready
			if len(pod.Status.PodIP) == 0 || string(pod.Status.Phase) != POD_PHASE_RUNNING {
				continue
			}

			// Wait for all containers in the pod to be ready
			podReady := true
			for _, cs := range pod.Status.ContainerStatuses {
				podReady = podReady && cs.Ready
			}

			// add it to the list of ready pods
			if podReady {
				readyPods = append(readyPods, &pod)
			}
		}
		log.Printf("[%v] found %v ready pods of %v total",
			newLabels, len(readyPods), len(podList.Items))

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
		chosenPod := readyPods[rand.Intn(len(readyPods))]

		// Relabel.  If the pod already got picked and modified, this should
		// fail; in that case just retry.
		chosenPod.ObjectMeta.Labels = newLabels

		// Remove the pod's replicaset/deployment owner reference; this will
		// allow it to be adopted by the rs/deployment that we create for the
		// function.
		chosenPod.ObjectMeta.OwnerReferences = nil

		log.Printf("relabeling pod: [%v]", chosenPod.ObjectMeta.Name)
		chosenPod, err = gp.kubernetesClient.Core().Pods(gp.namespace).Update(chosenPod)
		if err != nil {
			log.Printf("failed to relabel pod [%v]: %v", chosenPod.ObjectMeta.Name, err)
			continue
		}
		log.Printf("Chosen pod: %v (in %v)", chosenPod.ObjectMeta.Name, time.Now().Sub(startTime))
		return chosenPod, nil
	}
}

func (gp *GenericPool) labelsForFunction(metadata *fission.Metadata) map[string]string {
	return map[string]string{
		"functionName":           metadata.Name,
		"functionUid":            metadata.Uid,
		POOLMGR_INSTANCEID_LABEL: gp.instanceId,
	}
}

func (gp *GenericPool) scheduleDeletePod(name string) {
	go func() {
		// The sleep allows debugging or collecting logs from the pod before it's
		// cleaned up.  (We need a better solutions for both those things; log
		// aggregation and storage will help.)
		log.Printf("Error in pod '%v', scheduling cleanup", name)
		time.Sleep(5 * time.Minute)
		gp.kubernetesClient.Core().Pods(gp.namespace).Delete(name, nil)
	}()
}

// Deployment for a function.  The function pod is "adopted" into this
// deployment (in other words, the deployment is created after the
// pod).  This deployment allows us to scale the number of function
// instances up and down easily.
// The function pod needs to be labeled with the same template-hash as
// the newly created replica set in order to be adopted
func (gp *GenericPool) createFunctionDeployment(
	metadata *fission.Metadata, env *fission.Environment, funcLabels map[string]string, pod *v1.Pod) error {
	name := fmt.Sprintf("func-%v-%v", metadata.Name, metadata.Uid)
	fetcherRequest := gp.makeFetcherRequest(metadata)
	var initialReplicas int32 = 1
	sharedMountPath := "/userfunc"

	envResources := GetResourceQuantity(env)

	deployment := &v1beta1.Deployment{
		ObjectMeta: v1.ObjectMeta{
			Name:   name,
			Labels: funcLabels,
		},
		Spec: v1beta1.DeploymentSpec{
			Replicas: &initialReplicas,
			Selector: &v1beta1.LabelSelector{
				MatchLabels: funcLabels,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					Labels: funcLabels,
				},
				Spec: v1.PodSpec{
					Volumes: []v1.Volume{
						{
							Name: "userfunc",
							VolumeSource: v1.VolumeSource{
								EmptyDir: &v1.EmptyDirVolumeSource{},
							},
						},
					},
					Containers: []v1.Container{
						{
							Name:                   gp.env.Metadata.Name,
							Image:                  gp.env.RunContainerImageUrl,
							ImagePullPolicy:        v1.PullIfNotPresent,
							TerminationMessagePath: "/dev/termination-log",
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "userfunc",
									MountPath: sharedMountPath,
								},
							},
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									"memory": envResources.memLimit,
									"cpu":    envResources.cpuLimit,
								},
								Requests: v1.ResourceList{
									"memory": envResources.memRequest,
									"cpu":    envResources.cpuRequest,
								},
							},
						},
						{
							Name:                   "fetcher",
							Image:                  "yqf3139/fetcher",
							ImagePullPolicy:        v1.PullIfNotPresent,
							TerminationMessagePath: "/dev/termination-log",
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "userfunc",
									MountPath: sharedMountPath,
								},
							},
							Command: []string{"/fetcher", sharedMountPath},
							Env: []v1.EnvVar{
								{
									Name:  "FETCHER_REQUEST",
									Value: fetcherRequest,
								},
							},
							ReadinessProbe: &v1.Probe{
								Handler: v1.Handler{
									HTTPGet: &v1.HTTPGetAction{
										Path: "/ready",
										Port: intstr.FromInt(8000),
									},
								},
								InitialDelaySeconds: 1,
								PeriodSeconds:       1,
								FailureThreshold:    10,
							},
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									"memory": FETCHER_MEM_LIMIT,
									"cpu":    FETCHER_CPU_LIMIT,
								},
								Requests: v1.ResourceList{
									"memory": FETCHER_MEM_REQUEST,
									"cpu":    FETCHER_CPU_REQUEST,
								},
							},
						},
					},
				},
			},
		},
	}

	_, err := gp.kubernetesClient.Extensions().Deployments(gp.namespace).Create(deployment)
	if err != nil {
		return err
	}

	// try to fetch the corresponding replica set for 5 times
	var rs *v1beta1.ReplicaSet = nil
	for range [5]struct{}{} {
		rsList, err := gp.kubernetesClient.Extensions().ReplicaSets(gp.namespace).List(api.ListOptions{
			LabelSelector: labels.Set(funcLabels).AsSelector(),
		})
		if err != nil || len(rsList.Items) == 0 {
			fmt.Println("replicasets is nil or empty, retry later")
			time.Sleep(500 * time.Microsecond)
			continue
		}
		rs = &rsList.Items[0]
		break
	}
	if rs == nil {
		fmt.Printf("replicaset not found, label template-hash to pod [%v] failed", pod.ObjectMeta.Name)
		return nil
	}

	pod.Labels["pod-template-hash"] = rs.Labels["pod-template-hash"]
	_, err = gp.kubernetesClient.Core().Pods(gp.namespace).Update(pod)
	if err != nil {
		log.Printf("failed to add template hash to pod [%v]: %v", pod.ObjectMeta.Name, err)
	}
	return nil
}

func (gp *GenericPool) createHorizontalPodAutoscaler(metadata *fission.Metadata, cpuPercent, min, max int32) error {
	if cpuPercent < 1 || cpuPercent > 100 {
		cpuPercent = 60
	}
	if max < 1 {
		max = 3
	}

	hpaName := fmt.Sprintf("hpa-%v-%v", metadata.Name, metadata.Uid)
	deplName := fmt.Sprintf("func-%v-%v", metadata.Name, metadata.Uid)

	hpa := &autoscalingv1.HorizontalPodAutoscaler{
		ObjectMeta: v1.ObjectMeta{
			Name: hpaName,
		},
		Spec: autoscalingv1.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv1.CrossVersionObjectReference{
				Kind: "Deployment",
				Name: deplName,
			},
			MinReplicas:                    &min,
			MaxReplicas:                    max,
			TargetCPUUtilizationPercentage: &cpuPercent,
		},
	}

	_, err := gp.kubernetesClient.Autoscaling().HorizontalPodAutoscalers(gp.namespace).Create(hpa)
	return err
}

func (gp *GenericPool) makeFetcherRequest(m *fission.Metadata) string {
	functionUrl := fmt.Sprintf("%v/v1/functions/%v?uid=%v&raw=1",
		gp.controllerUrl, m.Name, m.Uid)
	return fmt.Sprintf("{\"url\": \"%v\", \"filename\": \"user\"}", functionUrl)
}

// specializePod chooses a pod, copies the required user-defined function to that pod
// (via fetcher), and calls the function-run container to load it, resulting in a
// specialized pod.
func (gp *GenericPool) specializePod(pod *v1.Pod, metadata *fission.Metadata) error {
	// for fetcher we don't need to create a service, just talk to the pod directly
	podIP := pod.Status.PodIP
	if len(podIP) == 0 {
		return errors.New("Pod has no IP")
	}

	// tell fetcher to get the function.
	fetcherUrl := fmt.Sprintf("http://%v:8000/", podIP)
	fetcherRequest := gp.makeFetcherRequest(metadata)

	log.Printf("[%v] calling fetcher to copy function", metadata)
	resp, err := http.Post(fetcherUrl, "application/json", bytes.NewReader([]byte(fetcherRequest)))
	if err != nil {
		// TODO we should retry this call in case fetcher hasn't come up yet
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New(fmt.Sprintf("Error from fetcher: %v", resp.Status))
	}

	// get function run container to specialize
	log.Printf("[%v] specializing pod", metadata)
	specializeUrl := fmt.Sprintf("http://%v:8888/specialize", podIP)

	// retry the specialize call a few times in case the env server hasn't come up yet
	maxRetries := 20
	for i := 0; i < maxRetries; i++ {
		resp2, err := http.Post(specializeUrl, "text/plain", bytes.NewReader([]byte{}))
		if err == nil && resp2.StatusCode < 300 {
			// Success
			resp2.Body.Close()
			return nil
		}

		// Only retry for the specific case of a connection error.
		if urlErr, ok := err.(*url.Error); ok {
			if netErr, ok := urlErr.Err.(*net.OpError); ok {
				if netErr.Op == "dial" {
					if i < maxRetries-1 {
						time.Sleep(500 * time.Duration(2*i) * time.Millisecond)
						log.Printf("Error connecting to pod (%v), retrying", netErr)
						continue
					}
				}
			}
		}

		if err == nil {
			err = fission.MakeErrorFromHTTP(resp2)
		}
		log.Printf("Failed to specialize pod: %v", err)
		return err
	}

	return nil
}

// A pool is a deployment of generic containers for an env.  This
// creates the pool but doesn't wait for any pods to be ready.
func (gp *GenericPool) createPool() error {
	poolDeploymentName := fmt.Sprintf("%v-%v-%v",
		gp.env.Metadata.Name, gp.env.Metadata.Uid, strings.ToLower(gp.poolInstanceId))

	sharedMountPath := "/userfunc"
	envResources := GetResourceQuantity(gp.env)

	deployment := &v1beta1.Deployment{
		ObjectMeta: v1.ObjectMeta{
			Name:   poolDeploymentName,
			Labels: gp.labelsForPool,
		},
		Spec: v1beta1.DeploymentSpec{
			Replicas: &gp.replicas,
			Selector: &v1beta1.LabelSelector{
				MatchLabels: gp.labelsForPool,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					Labels: gp.labelsForPool,
				},
				Spec: v1.PodSpec{
					Volumes: []v1.Volume{
						{
							Name: "userfunc",
							VolumeSource: v1.VolumeSource{
								EmptyDir: &v1.EmptyDirVolumeSource{},
							},
						},
					},
					Containers: []v1.Container{
						{
							Name:                   gp.env.Metadata.Name,
							Image:                  gp.env.RunContainerImageUrl,
							ImagePullPolicy:        v1.PullIfNotPresent,
							TerminationMessagePath: "/dev/termination-log",
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "userfunc",
									MountPath: sharedMountPath,
								},
							},
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									"memory": envResources.memLimit,
									"cpu":    envResources.cpuLimit,
								},
								Requests: v1.ResourceList{
									"memory": envResources.memRequest,
									"cpu":    envResources.cpuRequest,
								},
							},
						},
						{
							Name:                   "fetcher",
							Image:                  "fission/fetcher",
							ImagePullPolicy:        v1.PullIfNotPresent,
							TerminationMessagePath: "/dev/termination-log",
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "userfunc",
									MountPath: sharedMountPath,
								},
							},
							Command: []string{"/fetcher", sharedMountPath},
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									"memory": FETCHER_MEM_LIMIT,
									"cpu":    FETCHER_CPU_LIMIT,
								},
								Requests: v1.ResourceList{
									"memory": FETCHER_MEM_REQUEST,
									"cpu":    FETCHER_CPU_REQUEST,
								},
							},
						},
					},
				},
			},
		},
	}
	depl, err := gp.kubernetesClient.Extensions().Deployments(gp.namespace).Create(deployment)
	if err != nil {
		return err
	}
	gp.deployment = depl
	return nil
}

func (gp *GenericPool) waitForReadyPod() error {
	startTime := time.Now()
	for {
		// TODO: for now we just poll; use a watch instead
		depl, err := gp.kubernetesClient.Extensions().Deployments(gp.namespace).Get(gp.deployment.ObjectMeta.Name)
		if err != nil {
			log.Printf("err: %v", err)
			return err
		}
		gp.deployment = depl
		if gp.deployment.Status.AvailableReplicas > 0 {
			return nil
		}

		if time.Now().Sub(startTime) > gp.podReadyTimeout {
			return errors.New("timeout: waited too long for pod to be ready")
		}
		time.Sleep(1000 * time.Millisecond)
	}
}

func (gp *GenericPool) createSvc(name string, svcLabels map[string]string) (*v1.Service, error) {
	service := v1.Service{
		ObjectMeta: v1.ObjectMeta{
			Name: name,
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeClusterIP,
			Ports: []v1.ServicePort{
				{
					Protocol:   v1.ProtocolTCP,
					Port:       80,
					TargetPort: intstr.FromInt(8888),
				},
			},
			Selector: svcLabels,
		},
	}
	svc, err := gp.kubernetesClient.Core().Services(gp.namespace).Create(&service)
	return svc, err
}

func (gp *GenericPool) deleteSvc(name string) error {
	return gp.kubernetesClient.Core().Services(gp.namespace).Delete(name, nil)
}

func (gp *GenericPool) deleteFunctionDeployment(name string, funcLabels map[string]string) error {
	err := gp.kubernetesClient.Extensions().Deployments(gp.namespace).Delete(name, nil)
	if err != nil {
		log.Printf("Error destroying deployment: %v", err)
		return err
	}

	// Destroy ReplicaSet.  Pre-1.6 K8s versions don't do this
	// automatically but post-1.6 K8s will, and may beat us to it,
	// so don't error out if we fail.
	rsList, err := gp.kubernetesClient.Extensions().ReplicaSets(gp.namespace).List(api.ListOptions{
		LabelSelector: labels.Set(funcLabels).AsSelector(),
	})
	if len(rsList.Items) >= 0 {
		for _, rs := range rsList.Items {
			err = gp.kubernetesClient.Extensions().ReplicaSets(gp.namespace).Delete(rs.ObjectMeta.Name, nil)
			if err != nil {
				log.Printf("Error deleting replicaset, ignoring: %v", err)
			}
		}
	}

	// Destroy Pods.  See note above.
	podList, err := gp.kubernetesClient.Core().Pods(gp.namespace).List(api.ListOptions{
		LabelSelector: labels.Set(funcLabels).AsSelector(),
	})
	if len(podList.Items) >= 0 {
		for _, pod := range podList.Items {
			err = gp.kubernetesClient.Core().Pods(gp.namespace).Delete(pod.ObjectMeta.Name, nil)
			if err != nil {
				log.Printf("Error deleting pod, ignoring: %v", err)
			}
		}
	}
	return nil
}

func (gp *GenericPool) deleteHorizontalPodAutoscaler(name string) error {
	return gp.kubernetesClient.Autoscaling().HorizontalPodAutoscalers(gp.namespace).Delete(name, nil)
}

func (gp *GenericPool) GetFuncSvc(m *fission.Metadata, f *fission.Function, env *fission.Environment) (*funcSvc, error) {
	// Pick a pod from the pool
	log.Printf("[%v] Choosing pod from pool", m)
	newLabels := gp.labelsForFunction(m)
	pod, err := gp.choosePod(newLabels)
	if err != nil {
		return nil, err
	}

	// Specialize the chosen pod, i.e. load it with the requested function
	err = gp.specializePod(pod, m)
	if err != nil {
		gp.scheduleDeletePod(pod.ObjectMeta.Name)
		return nil, err
	}
	log.Printf("Specialized pod: %v", pod.ObjectMeta.Name)

	// Create a K8s service
	svcName := fmt.Sprintf("%v-%v", m.Name, m.Uid)
	_, err = gp.createSvc(svcName, newLabels)
	if err != nil {
		gp.scheduleDeletePod(pod.ObjectMeta.Name)
		return nil, err
	}

	// Create a deployment async that we can use to scale the function
	// instances
	go func() {
		// managed by the function deployment
		// for logs and other services
		err = gp.createFunctionDeployment(m, env, newLabels, pod)
		if err != nil {
			log.Printf("Error creating function deployment: %v", err)
		}

		// Create Autoscalers
		// Horizontal Pod Autoscalers for k8s to watch cpu usage
		// currently only cpu is supported by hpa
		// TODO add more custom metrics
		err = gp.createHorizontalPodAutoscaler(m, int32(f.CpuTarget), 1, int32(f.MaxInstance))
		if err != nil {
			log.Printf("Error creating horizontal pod autoscaler: %v", err)
		}
	}()

	// The fission router isn't in the same namespace, so return a
	// namespace-qualified hostname
	svcAddress := fmt.Sprintf("%v.%v", svcName, gp.namespace)
	podAddress := fmt.Sprintf("%v:8888", pod.Status.PodIP)

	fsvc := &funcSvc{
		function:    m,
		environment: gp.env,
		svcAddress:  svcAddress,
		podAddress:  podAddress,
		podName:     pod.ObjectMeta.Name,
		ctime:       time.Now(),
		atime:       time.Now(),
	}

	err, existingFsvc := gp.fsCache.Add(*fsvc)
	if err != nil {
		// Some other thread beat us to it -- return the other thread's fsvc and clean up
		// our own.  TODO: this is grossly inefficient, improve it with some sort of state
		// machine
		log.Printf("func svc already exists: %v", existingFsvc.podName)
		go func() {
			gp.kubernetesClient.Core().Pods(gp.namespace).Delete(fsvc.podName, nil)
		}()
		return existingFsvc, nil
	}
	return fsvc, nil
}

func (gp *GenericPool) CleanupFunctionService(m fission.Metadata) error {
	// remove ourselves from fsCache (only if we're still old)
	deleted, err := gp.fsCache.DeleteByFuncMeta(m, gp.idlePodReapTime)
	if err != nil {
		return err
	}

	if !deleted {
		log.Printf("Not deleting function %v, in use", m.Name)
		return nil
	}

	// delete service
	svcName := fmt.Sprintf("%v-%v", m.Name, m.Uid)
	err = gp.deleteSvc(svcName)
	if err != nil {
		log.Printf("Error deleting service for function: %v", err)
	}

	// delete deployment with replica set and all pods
	deplName := fmt.Sprintf("func-%v-%v", m.Name, m.Uid)
	err = gp.deleteFunctionDeployment(deplName, gp.labelsForFunction(&m))
	if err != nil {
		log.Printf("Error deleting deployment for function: %v", err)
	}

	// delete autoscaler
	// delete k8s Horizontal Pod Autoscalers
	hpaName := fmt.Sprintf("hpa-%v-%v", m.Name, m.Uid)
	err = gp.deleteHorizontalPodAutoscaler(hpaName)
	if err != nil {
		log.Printf("Error deleting horizontal pod autoscaler for function: %v", err)
	}

	return nil
}

func (gp *GenericPool) idlePodReaper() {
	for {
		time.Sleep(time.Minute)
		funcMetas, err := gp.fsCache.ListOld(gp.idlePodReapTime)
		if err != nil {
			log.Printf("Error reaping idle pods: %v", err)
			continue
		}
		for _, m := range funcMetas {
			log.Printf("Reaping idle function '%v'", m.Name)
			err := gp.CleanupFunctionService(m)
			if err != nil {
				log.Printf("Error deleting idle function '%v': %v", m.Name, err)
			}
		}
	}
}

// destroys the pool -- the deployment, replicaset and pods
func (gp *GenericPool) destroy() error {
	// Destroy deployment
	err := gp.kubernetesClient.Extensions().Deployments(gp.namespace).Delete(gp.deployment.ObjectMeta.Name, nil)
	if err != nil {
		log.Printf("Error destroying deployment: %v", err)
		return err
	}

	// Destroy ReplicaSet.  Pre-1.6 K8s versions don't do this
	// automatically but post-1.6 K8s will, and may beat us to it,
	// so don't error out if we fail.
	rsList, err := gp.kubernetesClient.Extensions().ReplicaSets(gp.namespace).List(api.ListOptions{
		LabelSelector: labels.Set(gp.labelsForPool).AsSelector(),
	})
	if len(rsList.Items) >= 0 {
		for _, rs := range rsList.Items {
			err = gp.kubernetesClient.Extensions().ReplicaSets(gp.namespace).Delete(rs.ObjectMeta.Name, nil)
			if err != nil {
				log.Printf("Error deleting replicaset, ignoring: %v", err)
			}
		}
	}

	// Destroy Pods.  See note above.
	podList, err := gp.kubernetesClient.Core().Pods(gp.namespace).List(api.ListOptions{
		LabelSelector: labels.Set(gp.labelsForPool).AsSelector(),
	})
	if len(podList.Items) >= 0 {
		for _, pod := range podList.Items {
			err = gp.kubernetesClient.Core().Pods(gp.namespace).Delete(pod.ObjectMeta.Name, nil)
			if err != nil {
				log.Printf("Error deleting pod, ignoring: %v", err)
			}
		}
	}

	return nil
}
