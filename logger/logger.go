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

package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	log "github.com/Sirupsen/logrus"
	"github.com/fission/fission"
	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/pkg/api"
	"k8s.io/client-go/1.5/pkg/api/unversioned"
	"k8s.io/client-go/1.5/pkg/api/v1"
	"k8s.io/client-go/1.5/pkg/watch"
	"k8s.io/client-go/1.5/rest"
)

func makelogRequestTracker() logRequestTracker {
	return logRequestTracker{
		logMap: make(map[string]LogRequest),
	}
}

func (l logRequestTracker) Add(logReq LogRequest) {
	l.Lock()
	l.logMap[logReq.Pod] = logReq
	l.Unlock()
}

func (l logRequestTracker) Get(pod string) LogRequest {
	l.RLock()
	logReq, ok := l.logMap[pod]
	l.RUnlock()
	if ok {
		return logReq
	}
	return LogRequest{}
}

func (l logRequestTracker) Remove(logReq LogRequest) {
	l.Lock()
	delete(l.logMap, logReq.Pod)
	l.Unlock()
}

// Get a kubernetes client using the pod's service account.  This only
// works when we're running inside a kubernetes cluster.
func getKubernetesClient() (*kubernetes.Clientset, error) {
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Printf("Error getting kubernetes client config: %v", err)
		return nil, err
	}

	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Printf("Error getting kubernetes client: %v", err)
		return nil, err
	}

	return clientset, nil
}

// make sure that the targetPath is a legitimate path for security purpose
func validateFilePath(targetPath string, expectedPathPrefix string) bool {
	targetPath = filepath.Clean(targetPath)
	return strings.HasPrefix(targetPath, expectedPathPrefix)
}

// The ContainerID is consist of container engine type (docker://) and uuid of container.
// (e.g., docker://f4ca66baaa715030e20273aaf5232635a144165f1cd8e34ca5175064c245b679)
// This function tries to extract container uuid from ContainerID.
func parseContainerString(containerID string) (string, error) {
	// Trim the quotes and split the type and ID.
	parts := strings.Split(strings.Trim(containerID, "\""), "://")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid container ID: %q", containerID)
	}
	_, ID := parts[0], parts[1]
	return ID, nil
}

func getcontainerID(kubeClient *kubernetes.Clientset, namespace, pod, container string) (string, error) {
	podInfo, err := kubeClient.Core().Pods(namespace).Get(pod)
	if err != nil {
		log.Printf("Failed to get pod info: %v, %v", err, pod)
		return "", err
	}
	var containerID string
	for _, c := range podInfo.Status.ContainerStatuses {
		if c.Name == container {
			containerID, err = parseContainerString(c.ContainerID)
			if err != nil {
				log.Printf("Failed to get container id: %v, %v", err, pod)
				return "", err
			}
			return containerID, nil
		}
	}
	return "", fission.MakeError(404, "no matching container is found")
}

func getContainerLogPath(logReq LogRequest) (string, bool) {
	logPath := fmt.Sprintf("/var/lib/docker/containers/%s/%s-json.log", logReq.ContainerID, logReq.ContainerID)
	if !validateFilePath(logPath, "/var/lib/docker/containers") {
		return "", false
	}
	return logPath, true
}

func getFissionLogSymlinkPath(logReq LogRequest) (string, bool) {
	// pass function related information through a symlink name
	logSymLink := fmt.Sprintf("/var/log/fission/%s.%s.%s.%s.%s.log", logReq.Namespace, logReq.Pod, logReq.ContainerID, logReq.FuncName, logReq.FuncUid)
	if !validateFilePath(logSymLink, "/var/log/fission") {
		return "", false
	}
	return logSymLink, true
}

func createLogSymlink(pod *v1.Pod, kubernetesClient *kubernetes.Clientset) {
	if !isPodReady(pod) {
		return
	}
	logReq := logInfo.Get(pod.Name)
	if logReq.Pod != "" {
		return
	}
	logReq = LogRequest{
		Namespace: pod.Namespace,
		Pod:       pod.Name,
		Container: pod.Spec.Containers[0].Name,
		FuncName:  pod.Labels["functionName"],
		FuncUid:   pod.Labels["functionUid"],
	}

	log.Infof("Log symlink start: %v", pod.Name)
	containerID, err := getcontainerID(kubernetesClient, logReq.Namespace, logReq.Pod, logReq.Container)
	if err != nil || containerID == "" {
		return
	}

	logReq.ContainerID = containerID
	containerLogFilePath, isValidLogPath := getContainerLogPath(logReq)
	fissionLogSymlinkPath, isValidSymlinkPath := getFissionLogSymlinkPath(logReq)
	if !isValidLogPath || !isValidSymlinkPath {
		log.Errorf("Log or symlink path is not valid")
		return
	}

	err = os.Symlink(containerLogFilePath, fissionLogSymlinkPath)
	if err != nil {
		log.Errorf("Log or symlink path is not valid", err)
		return
	}
	logInfo.Add(logReq)
	log.Infof("Log symlink created: %v", pod.Name)
}

func removeLogSymlink(pod string) {
	logReq := logInfo.Get(pod)
	if logReq.Pod == "" {
		return
	}

	fissionLogSymlinkPath, isValidSymlinkPath := getFissionLogSymlinkPath(logReq)
	if !isValidSymlinkPath {
		log.Errorf("The pod symlink path is not valid: %v", pod)
		return
	}
	err := os.Remove(fissionLogSymlinkPath)
	if err != nil {
		log.Errorf("Failed to remove the pod symlink: %v", err)
		return
	}
	log.Infof("Log symlink removed: %v", pod)
}

func isPodReady(pod *v1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == v1.PodReady {
			return true
		}
	}
	return false
}

var logInfo logRequestTracker

func Start(namespace string) {
	logInfo = makelogRequestTracker()

	kubernetesClient, err := getKubernetesClient()
	if err != nil {
		log.Errorf("Failed to get kubernetes client: %v", err)
		return
	}

	// TODO find a better way to get the node name for this pod
	hostname := os.Getenv("HOSTNAME")
	self, err := kubernetesClient.Core().Pods("fission").Get(hostname)
	if err != nil {
		log.Errorf("Failed to get pod self reference: %v", err)
		return
	}

	listOptions := api.ListOptions{}
	wi, err := kubernetesClient.Core().Pods(namespace).Watch(listOptions)
	if err != nil {
		log.Errorf("Failed to watch pods: %v", err)
		return
	}

	for {
		ev, more := <-wi.ResultChan()
		if !more {
			log.Errorf("Watch stopped")
			break
		}
		switch evType := ev.Object.(type) {
		case *v1.Pod:
			pod := ev.Object.(*v1.Pod)
			if self.Spec.NodeName != pod.Spec.NodeName {
				break
			}
			if _, fond := pod.Labels["functionName"]; !fond {
				break
			}

			switch ev.Type {
			case watch.Added:
				break
			case watch.Deleted:
				removeLogSymlink(pod.Name)
				break
			case watch.Modified:
				// TODO filter out the pod which is created and then deleted instantly
				// which is caused by a deployment with 1 replica
				// and then a generic pod relabeled by fission is adopted
				createLogSymlink(pod, kubernetesClient)
				break
			case watch.Error:
				break
			}
			break
		case *unversioned.Status:
			log.Warnf("received unversioned status", ev.Object.(*unversioned.Status))
			break
		default:
			log.Warnf("unknown type,", evType)
			wi.Stop()
			wi, err = kubernetesClient.Core().Pods(namespace).Watch(listOptions)
			if err != nil {
				log.Errorf("Failed to watch pods: %v", err)
				return
			}
			break
		}
	}
}
