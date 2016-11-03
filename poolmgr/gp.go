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
	"time"

	"k8s.io/client-go/1.4/kubernetes"
	"k8s.io/client-go/1.4/pkg/api"
	"k8s.io/client-go/1.4/pkg/api/v1"
	"k8s.io/client-go/1.4/pkg/apis/extensions/v1beta1"
	"k8s.io/client-go/1.4/pkg/labels"
	"k8s.io/client-go/1.4/pkg/util/intstr"

	"github.com/platform9/fission"
)

type (
	GenericPool struct {
		env             *fission.Environment
		replicas        int32               // num containers
		deployment      *v1beta1.Deployment // kubernetes deployment
		namespace       string              // namespace to keep our resources
		podReadyTimeout time.Duration       // timeout for generic pods to become ready
		controllerUrl   string

		kubernetesClient *kubernetes.Clientset
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
	namespace string) (*GenericPool, error) {

	log.Printf("Creating pool for environment %v", env.Metadata)
	gp := &GenericPool{
		env:              env,
		replicas:         initialReplicas,
		requestChannel:   make(chan *choosePodRequest),
		kubernetesClient: kubernetesClient,
		namespace:        namespace,
		podReadyTimeout:  5 * time.Minute,
		controllerUrl:    controllerUrl,
	}

	// create the pool
	err := gp.createPool()
	if err != nil {
		return nil, err
	}

	// wait for at least one pod to be ready
	log.Printf("[%v] Deployment created, waiting for a ready pod", env.Metadata)
	err = gp.waitForReadyPod()
	if err != nil {
		return nil, err
	}

	go gp.choosePodService()
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
		for _, pod := range podList.Items {
			podReady := true
			for _, cs := range pod.Status.ContainerStatuses {
				podReady = podReady && cs.Ready
			}
			if podReady {
				log.Printf("pod %v is ready", pod.ObjectMeta.Name)
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

		// Relabel.  If the pod already got picked and
		// modified, this should fail; in that case just
		// retry.
		chosenPod.ObjectMeta.Labels = newLabels
		log.Printf("relabeling pod: [%v]", chosenPod.ObjectMeta.Name)
		_, err = gp.kubernetesClient.Core().Pods(gp.namespace).Update(chosenPod)
		if err != nil {
			log.Printf("failed to relabel pod [%v]: %v", chosenPod.ObjectMeta.Name, err)
			continue
		}
		log.Printf("Chosen pod: %v (in %v)", chosenPod.ObjectMeta.Name, time.Now().Sub(startTime))
		return chosenPod, nil
	}
}

func labelsForMetadata(metadata *fission.Metadata) map[string]string {
	return map[string]string{
		"functionName": metadata.Name,
		"functionUid":  metadata.Uid,
	}
}

// specializePod chooses a pod, copies the required user-defined function to that pod
// (via fetcher), and calls the function-run container to load it, resulting in a
// specialized pod.
func (gp *GenericPool) specializePod(metadata *fission.Metadata) (*v1.Pod, error) {
	newLabels := labelsForMetadata(metadata)

	log.Printf("[%v] Choosing pod from pool", metadata)
	pod, err := gp.choosePod(newLabels)
	if err != nil {
		return nil, err
	}

	// for fetcher we don't need to create a service, just talk to the pod directly
	podIP := pod.Status.PodIP
	if len(podIP) == 0 {
		return nil, errors.New("Pod has no IP")
	}

	// tell fetcher to get the function
	fetcherUrl := fmt.Sprintf("http://%v:8000/", podIP)
	functionUrl := fmt.Sprintf("%v/v1/functions/%v?uid=%v&raw=1",
		gp.controllerUrl, metadata.Name, metadata.Uid)
	fetcherRequest := fmt.Sprintf("{\"url\": \"%v\", \"filename\": \"user\"}", functionUrl)

	log.Printf("[%v] calling fetcher to copy function", metadata)
	resp, err := http.Post(fetcherUrl, "application/json", bytes.NewReader([]byte(fetcherRequest)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New(fmt.Sprintf("Error from fetcher: %v", resp.Status))
	}

	// get function run container to specialize
	log.Printf("[%v] specializing pod", metadata)
	specializeUrl := fmt.Sprintf("http://%v:8888/specialize", podIP)

	// retry the specialize call a few times in case the env server hasn't come up yet
	maxRetries := 20
	for i := 0; i < maxRetries; i++ {
		resp2, err := http.Post(specializeUrl, "text/plain", bytes.NewReader([]byte{}))
		if err != nil {
			if urlErr, ok := err.(*url.Error); ok {
				if netErr, ok := urlErr.Err.(*net.OpError); ok {
					if netErr.Op == "dial" { // && netErr.Err == syscall.ECONNREFUSED
						log.Printf("Error connecting to pod (%v)", netErr)
						if i < maxRetries-1 {
							time.Sleep(500 * time.Duration(2*i) * time.Millisecond)
							continue
						}
					}
				}
			}
			return nil, err
		}
		resp2.Body.Close()
	}

	return pod, nil
}

// A pool is a deployment of generic containers for an env.  This
// creates the pool but doesn't wait for any pods to be ready.
func (gp *GenericPool) createPool() error {
	poolDeploymentName := fmt.Sprintf("deployment-%v-%v-0",
		gp.env.Metadata.Name, gp.env.Metadata.Uid)

	podLabels := map[string]string{
		"pool": poolDeploymentName,
	}

	sharedMountPath := "/userfunc"
	deployment := &v1beta1.Deployment{
		ObjectMeta: v1.ObjectMeta{
			Name: poolDeploymentName,
			Labels: map[string]string{
				"environmentName": gp.env.Metadata.Name,
				"environmentUid":  gp.env.Metadata.Uid,
			},
		},
		Spec: v1beta1.DeploymentSpec{
			Replicas: &gp.replicas,
			Selector: &v1beta1.LabelSelector{
				MatchLabels: podLabels,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					Labels: podLabels,
				},
				Spec: v1.PodSpec{
					Volumes: []v1.Volume{
						v1.Volume{
							Name: "userfunc",
							VolumeSource: v1.VolumeSource{
								EmptyDir: &v1.EmptyDirVolumeSource{},
							},
						},
					},
					Containers: []v1.Container{
						v1.Container{
							Name:                   gp.env.Metadata.Name,
							Image:                  gp.env.RunContainerImageUrl,
							ImagePullPolicy:        v1.PullIfNotPresent,
							TerminationMessagePath: "/dev/termination-log",
							VolumeMounts: []v1.VolumeMount{
								v1.VolumeMount{
									Name:      "userfunc",
									MountPath: sharedMountPath,
								},
							},
						},
						v1.Container{
							Name:                   "fetcher",
							Image:                  "fission/fetcher",
							ImagePullPolicy:        v1.PullIfNotPresent,
							TerminationMessagePath: "/dev/termination-log",
							VolumeMounts: []v1.VolumeMount{
								v1.VolumeMount{
									Name:      "userfunc",
									MountPath: sharedMountPath,
								},
							},
							Command: []string{"/fetcher", sharedMountPath},
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

func (gp *GenericPool) createSvc(name string, labels map[string]string) (*v1.Service, error) {
	service := v1.Service{
		ObjectMeta: v1.ObjectMeta{
			Name: name,
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeClusterIP,
			Ports: []v1.ServicePort{
				v1.ServicePort{
					Protocol:   v1.ProtocolTCP,
					Port:       80,
					TargetPort: intstr.FromInt(8888),
				},
			},
			Selector: labels,
		},
	}
	svc, err := gp.kubernetesClient.Core().Services(gp.namespace).Create(&service)
	return svc, err
}

func (gp *GenericPool) GetFuncSvc(m *fission.Metadata) (*funcSvc, error) {
	pod, err := gp.specializePod(m)
	if err != nil {
		return nil, err
	}
	log.Printf("Specialized pod: %v", pod.ObjectMeta.Name)

	svcName := fmt.Sprintf("svc-%v", m.Name)
	if len(m.Uid) > 0 {
		svcName += ("-" + m.Uid)
	}

	labels := labelsForMetadata(m)
	svc, err := gp.createSvc(svcName, labels)
	if err != nil {
		return nil, err
	}
	if svc.ObjectMeta.Name != svcName {
		return nil, errors.New(fmt.Sprintf("sanity check failed for svc %v", svc.ObjectMeta.Name))
	}

	// the fission router isn't in the same namespace, so return a
	// namespace-qualified hostname
	svcHost := fmt.Sprintf("%v.%v", svcName, gp.namespace)

	fsvc := &funcSvc{
		function:    m,
		environment: gp.env,
		serviceName: svcHost,
		ctime:       time.Now(),
		atime:       time.Now(),
	}
	return fsvc, nil
}
