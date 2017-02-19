package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"sync"

	log "github.com/Sirupsen/logrus"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/rest"
)

type (
	logRequest struct {
		Namespace   string `json:"namespace"`
		Pod         string `json:"pod"`
		Container   string `json:"container"`
		FuncUid     string `json:"funcuid"`
		ContainerID string `json:"-"`
	}

	logRequestHandler struct {
		sync.RWMutex
		logMap map[string]logRequest
	}
)

func newLogInfoMap() logRequestHandler {
	return logRequestHandler{
		logMap: make(map[string]logRequest),
	}
}

func (l logRequestHandler) Add(logReq logRequest) {
	l.Lock()
	l.logMap[logReq.Pod] = logReq
	l.Unlock()
}

func (l logRequestHandler) Get(pod string) logRequest {
	l.RLock()
	logReq, ok := l.logMap[pod]
	l.RUnlock()
	if ok {
		return logReq
	}
	return logRequest{}
}

func (l logRequestHandler) Remove(logReq logRequest) {
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

func ParseContainerString(data string) (string, error) {
	// Trim the quotes and split the type and ID.
	parts := strings.Split(strings.Trim(data, "\""), "://")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid container ID: %q", data)
	}
	_, ID := parts[0], parts[1]
	return ID, nil
}

func GetcontainerID(kubeClient *kubernetes.Clientset, namespace, pod, container string) (string, error) {
	podInfo, err := kubeClient.Core().Pods(namespace).Get(pod)
	if err != nil {
		log.Printf("Failed to get pod info: %v", err)
		return "", err
	}
	var containerID string
	for _, c := range podInfo.Status.ContainerStatuses {
		if c.Name == container {
			containerID, err = ParseContainerString(c.ContainerID)
			if err != nil {
				log.Printf("Failed to get container id: %v", err)
				return "", nil
			}
			return containerID, nil
		}
	}
	return "", nil
}

func createLogSymlink(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request", 500)
		return
	}
	logReq := logRequest{}
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

	containerID, err := GetcontainerID(kubernetesClient, logReq.Namespace, logReq.Pod, logReq.Container)
	if err != nil || containerID == "" {
		log.Warningf("Failed to get container id: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	logReq.ContainerID = containerID
	containerLogFile := fmt.Sprintf("/var/lib/docker/containers/%s/%s-json.log", logReq.ContainerID, logReq.ContainerID)
	fissionLogSymlink := fmt.Sprintf("/var/log/fission/%s.%s.%s.%s", logReq.Namespace, logReq.Pod, logReq.ContainerID, logReq.FuncUid)
	err = os.Symlink(containerLogFile, fissionLogSymlink)
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

	fissionLogSymlink := fmt.Sprintf("/var/log/fission/%s.%s.%s.%s", logReq.Namespace, logReq.Pod, logReq.ContainerID, logReq.FuncUid)
	err := os.Remove(fissionLogSymlink)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

var logInfo logRequestHandler

func main() {
	logInfo = newLogInfoMap()
	r := mux.NewRouter()
	r.HandleFunc("/v1/log", createLogSymlink).Methods("POST")
	r.HandleFunc("/v1/log/{pod}", removeLogSymlink).Methods("DELETE")
	address := fmt.Sprintf(":%v", 1234)
	log.Printf("starting poolmgr at port %s", address)
	log.Fatal(http.ListenAndServe(address, handlers.LoggingHandler(os.Stdout, r)))
}
