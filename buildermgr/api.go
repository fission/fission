/*
Copyright 2017 The Fission Authors.

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

package buildermgr

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/pkg/api"

	"github.com/fission/fission/tpr"
)

const (
	EnvBuilderNamespace = "fission-builder"
)

type (
	BuildRequest struct {
		Package api.ObjectMeta `json:"package"`
	}

	BuilderMgr struct {
		fissionClient    *tpr.FissionClient
		kubernetesClient *kubernetes.Clientset
		storageSvcUrl    string
		namespace        string
	}
)

func MakeBuilderMgr(fissionClient *tpr.FissionClient,
	kubernetesClient *kubernetes.Clientset, storageSvcUrl string) *BuilderMgr {

	envWatcher := makeEnvironmentWatcher(fissionClient, kubernetesClient, EnvBuilderNamespace)
	go envWatcher.watchEnvironments()

	pkgWatcher := makePackageWatcher(fissionClient, kubernetesClient, EnvBuilderNamespace, storageSvcUrl)
	go pkgWatcher.watchPackages()

	return &BuilderMgr{
		fissionClient:    fissionClient,
		kubernetesClient: kubernetesClient,
		storageSvcUrl:    storageSvcUrl,
		namespace:        EnvBuilderNamespace,
	}
}

func (builderMgr *BuilderMgr) build(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		e := fmt.Sprintf("Failed to read request: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}

	buildReq := BuildRequest{}
	err = json.Unmarshal([]byte(body), &buildReq)
	if err != nil {
		e := fmt.Sprintf("invalid request body: %v", err)
		log.Println(e)
		http.Error(w, e, 400)
		return
	}

	httpCode, buildLogs, err := buildPackage(builderMgr.fissionClient, builderMgr.kubernetesClient,
		builderMgr.namespace, builderMgr.storageSvcUrl, buildReq)
	if err != nil {
		http.Error(w, err.Error(), httpCode)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	_, err = w.Write([]byte(buildLogs))
	if err != nil {
		e := fmt.Sprintf("Failed to reply http request: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
	}
}

func (builderMgr *BuilderMgr) Serve(port int) {
	r := mux.NewRouter()
	r.HandleFunc("/v1/build", builderMgr.build).Methods("POST")
	address := fmt.Sprintf(":%v", port)
	log.Printf("Start buildermgr at port %v", address)
	log.Fatal(http.ListenAndServe(address, handlers.LoggingHandler(os.Stdout, r)))
}
