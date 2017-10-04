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
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/fission/fission"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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
	podInfo, err := kubeClient.CoreV1().Pods(namespace).Get(pod, v1.GetOptions{})
	if err != nil {
		log.Printf("Failed to get pod info: %v", err)
		return "", err
	}
	var containerID string
	for _, c := range podInfo.Status.ContainerStatuses {
		if c.Name == container {
			containerID, err = parseContainerString(c.ContainerID)
			if err != nil {
				log.Printf("Failed to get container id: %v", err)
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

func createLogSymlink(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request", 500)
		return
	}
	logReq := LogRequest{}
	if err = json.Unmarshal(body, &logReq); err != nil {
		w.Write([]byte(fmt.Sprintf("%v", err)))
		return
	}

	kubernetesClient, err := getKubernetesClient()
	if err != nil {
		log.Warningf("Failed to get kubernetes client: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	containerID, err := getcontainerID(kubernetesClient, logReq.Namespace, logReq.Pod, logReq.Container)
	if err != nil || containerID == "" {
		log.Warningf("Failed to get container id: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	logReq.ContainerID = containerID
	containerLogFilePath, isValidLogPath := getContainerLogPath(logReq)
	fissionLogSymlinkPath, isValidSymlinkPath := getFissionLogSymlinkPath(logReq)
	if !isValidLogPath || !isValidSymlinkPath {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	err = os.Symlink(containerLogFilePath, fissionLogSymlinkPath)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	logInfo.Add(logReq)
	w.WriteHeader(http.StatusOK)
}

func removeLogSymlink(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	pod := vars["pod"]
	logReq := logInfo.Get(pod)
	if logReq.Pod == "" {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	fissionLogSymlinkPath, isValidSymlinkPath := getFissionLogSymlinkPath(logReq)
	if !isValidSymlinkPath {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	err := os.Remove(fissionLogSymlinkPath)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

var logInfo logRequestTracker

func Start() {
	logInfo = makelogRequestTracker()
	r := mux.NewRouter()
	r.HandleFunc("/v1/log", createLogSymlink).Methods("POST")
	r.HandleFunc("/v1/log/{pod}", removeLogSymlink).Methods("DELETE")
	address := fmt.Sprintf(":%v", 1234)
	log.Printf("starting logger at port %s", address)
	log.Fatal(http.ListenAndServe(address, handlers.LoggingHandler(os.Stdout, r)))
}
