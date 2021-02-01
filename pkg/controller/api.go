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

package controller

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/gorilla/mux"
	"go.uber.org/zap"
	apiv1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fission/fission/pkg/crd"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/fission-cli/logdb"
	"github.com/fission/fission/pkg/info"
)

var podNamespace string

func init() {
	podNamespace = os.Getenv("POD_NAMESPACE")
	if podNamespace == "" {
		podNamespace = "fission"
	}
}

type (
	API struct {
		logger            *zap.Logger
		fissionClient     *crd.FissionClient
		kubernetesClient  *kubernetes.Clientset
		storageServiceUrl string
		builderManagerUrl string
		workflowApiUrl    string
		functionNamespace string
		featureStatus     map[string]string
	}

	logDBConfig struct {
		httpURL  string
		username string
		password string
	}
)

func MakeAPI(logger *zap.Logger, featureStatus map[string]string) (*API, error) {
	api, err := makeCRDBackedAPI(logger)

	u := os.Getenv("STORAGE_SERVICE_URL")
	if len(u) > 0 {
		api.storageServiceUrl = strings.TrimSuffix(u, "/")
	} else {
		api.storageServiceUrl = "http://storagesvc"
	}

	u = os.Getenv("BUILDER_MANAGER_URL")
	if len(u) > 0 {
		api.builderManagerUrl = strings.TrimSuffix(u, "/")
	} else {
		api.builderManagerUrl = "http://buildermgr"
	}

	wfEnv := os.Getenv("WORKFLOW_API_URL")
	if len(u) > 0 {
		api.workflowApiUrl = strings.TrimSuffix(wfEnv, "/")
	} else {
		api.workflowApiUrl = "http://workflows-apiserver"
	}

	fnNs := os.Getenv("FISSION_FUNCTION_NAMESPACE")
	if len(fnNs) > 0 {
		api.functionNamespace = fnNs
	} else {
		api.functionNamespace = "fission-function"
	}

	api.featureStatus = featureStatus

	return api, err
}

func (api *API) respondWithSuccess(w http.ResponseWriter, resp []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, err := w.Write(resp)
	if err != nil {
		// this will probably fail too, but try anyway
		api.respondWithError(w, err)
	}
}

func (api *API) respondWithError(w http.ResponseWriter, err error) {
	// this error type comes with an HTTP code, so just use that
	se, ok := err.(*kerrors.StatusError)
	if ok {
		api.logger.Error(err.Error(), zap.Int32("code", se.ErrStatus.Code))
		http.Error(w, se.Error(), int(se.ErrStatus.Code))
		return
	}

	code, msg := ferror.GetHTTPError(err)
	api.logger.Error(msg, zap.Int("code", code))
	http.Error(w, msg, code)
}

func (api *API) extractQueryParamFromRequest(r *http.Request, queryParam string) string {
	values := r.URL.Query()
	return values.Get(queryParam)
}

// check if namespace exists, if not create it.
func (api *API) createNsIfNotExists(ns string) error {
	if ns == metav1.NamespaceDefault {
		// we don't have to create default ns
		return nil
	}

	_, err := api.kubernetesClient.CoreV1().Namespaces().Get(ns, metav1.GetOptions{})
	if err != nil && kerrors.IsNotFound(err) {
		ns := &apiv1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: ns,
			},
		}
		_, err = api.kubernetesClient.CoreV1().Namespaces().Create(ns)
	}

	return err
}

func (api *API) getLogDBConfig(dbType string) logDBConfig {
	dbType = strings.ToUpper(dbType)
	// retrieve db auth config from the env
	url := os.Getenv(fmt.Sprintf("%s_URL", dbType))
	if url == "" {
		// set up default database url
		url = logdb.INFLUXDB_URL
	}
	username := os.Getenv(fmt.Sprintf("%s_USERNAME", dbType))
	password := os.Getenv(fmt.Sprintf("%s_PASSWORD", dbType))
	return logDBConfig{
		httpURL:  url,
		username: username,
		password: password,
	}
}

func (api *API) HomeHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, err := w.Write([]byte(info.ApiInfo().String()))
	if err != nil {
		api.respondWithError(w, err)
	}
}

func (api *API) ApiVersionMismatchHandler(w http.ResponseWriter, r *http.Request) {
	err := ferror.MakeError(ferror.ErrorNotFound, "Fission server supports API v2 only -- v1 is not supported. Please upgrade your Fission client/CLI.")
	api.respondWithError(w, err)
}

func (api *API) HealthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (api *API) GetSvcName(w http.ResponseWriter, r *http.Request) {
	appLabelSelector := "application=" + r.URL.Query().Get("application")
	services, err := api.kubernetesClient.CoreV1().Services(podNamespace).List(metav1.ListOptions{
		LabelSelector: appLabelSelector,
	})
	if err != nil || len(services.Items) > 1 || len(services.Items) == 0 {
		api.respondWithError(w, err)
	}
	service := services.Items[0]
	fmt.Fprintf(w, service.Name+"."+podNamespace)
}

func (api *API) GetHandler() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/healthz", api.HealthHandler).Methods("GET")
	// Give a useful error message if an older CLI attempts to make a request
	r.HandleFunc(`/v1/{rest:[a-zA-Z0-9=\-\/]+}`, api.ApiVersionMismatchHandler)
	r.HandleFunc("/", api.HomeHandler)

	r.HandleFunc("/v2/packages", api.PackageApiList).Methods("GET")
	r.HandleFunc("/v2/packages", api.PackageApiCreate).Methods("POST")
	r.HandleFunc("/v2/packages/{package}", api.PackageApiGet).Methods("GET")
	r.HandleFunc("/v2/packages/{package}", api.PackageApiUpdate).Methods("PUT")
	r.HandleFunc("/v2/packages/{package}", api.PackageApiDelete).Methods("DELETE")

	r.HandleFunc("/v2/functions", api.FunctionApiList).Methods("GET")
	r.HandleFunc("/v2/functions", api.FunctionApiCreate).Methods("POST")
	r.HandleFunc("/v2/functions/{function}", api.FunctionApiGet).Methods("GET")
	r.HandleFunc("/v2/functions/{function}", api.FunctionApiUpdate).Methods("PUT")
	r.HandleFunc("/v2/functions/{function}", api.FunctionApiDelete).Methods("DELETE")

	r.HandleFunc("/v2/triggers/http", api.HTTPTriggerApiList).Methods("GET")
	r.HandleFunc("/v2/triggers/http", api.HTTPTriggerApiCreate).Methods("POST")
	r.HandleFunc("/v2/triggers/http/{httpTrigger}", api.HTTPTriggerApiGet).Methods("GET")
	r.HandleFunc("/v2/triggers/http/{httpTrigger}", api.HTTPTriggerApiUpdate).Methods("PUT")
	r.HandleFunc("/v2/triggers/http/{httpTrigger}", api.HTTPTriggerApiDelete).Methods("DELETE")

	r.HandleFunc("/v2/environments", api.EnvironmentApiList).Methods("GET")
	r.HandleFunc("/v2/environments", api.EnvironmentApiCreate).Methods("POST")
	r.HandleFunc("/v2/environments/{environment}", api.EnvironmentApiGet).Methods("GET")
	r.HandleFunc("/v2/environments/{environment}", api.EnvironmentApiUpdate).Methods("PUT")
	r.HandleFunc("/v2/environments/{environment}", api.EnvironmentApiDelete).Methods("DELETE")

	r.HandleFunc("/v2/watches", api.WatchApiList).Methods("GET")
	r.HandleFunc("/v2/watches", api.WatchApiCreate).Methods("POST")
	r.HandleFunc("/v2/watches/{watch}", api.WatchApiGet).Methods("GET")
	r.HandleFunc("/v2/watches/{watch}", api.WatchApiUpdate).Methods("PUT")
	r.HandleFunc("/v2/watches/{watch}", api.WatchApiDelete).Methods("DELETE")

	r.HandleFunc("/v2/triggers/time", api.TimeTriggerApiList).Methods("GET")
	r.HandleFunc("/v2/triggers/time", api.TimeTriggerApiCreate).Methods("POST")
	r.HandleFunc("/v2/triggers/time/{timeTrigger}", api.TimeTriggerApiGet).Methods("GET")
	r.HandleFunc("/v2/triggers/time/{timeTrigger}", api.TimeTriggerApiUpdate).Methods("PUT")
	r.HandleFunc("/v2/triggers/time/{timeTrigger}", api.TimeTriggerApiDelete).Methods("DELETE")

	r.HandleFunc("/v2/triggers/messagequeue", api.MessageQueueTriggerApiList).Methods("GET")
	r.HandleFunc("/v2/triggers/messagequeue", api.MessageQueueTriggerApiCreate).Methods("POST")
	r.HandleFunc("/v2/triggers/messagequeue/{mqTrigger}", api.MessageQueueTriggerApiGet).Methods("GET")
	r.HandleFunc("/v2/triggers/messagequeue/{mqTrigger}", api.MessageQueueTriggerApiUpdate).Methods("PUT")
	r.HandleFunc("/v2/triggers/messagequeue/{mqTrigger}", api.MessageQueueTriggerApiDelete).Methods("DELETE")

	r.HandleFunc("/v2/secrets/{secret}", api.SecretExists).Methods("GET")
	r.HandleFunc("/v2/configmaps/{configmap}", api.ConfigMapExists).Methods("GET")

	r.HandleFunc("/v2/canaryconfigs", api.CanaryConfigApiCreate).Methods("POST")
	r.HandleFunc("/v2/canaryconfigs/{canaryConfig}", api.CanaryConfigApiGet).Methods("GET")
	r.HandleFunc("/v2/canaryconfigs/{canaryConfig}", api.CanaryConfigApiUpdate).Methods("PUT")
	r.HandleFunc("/v2/canaryconfigs/{canaryConfig}", api.CanaryConfigApiDelete).Methods("DELETE")
	r.HandleFunc("/v2/canaryconfigs", api.CanaryConfigApiList).Methods("GET")

	r.HandleFunc("/proxy/{dbType}", api.FunctionLogsApiPost).Methods("POST")
	r.HandleFunc("/proxy/storage/v1/archive", api.StorageServiceProxy)
	r.HandleFunc("/proxy/logs/{function}", api.FunctionPodLogs).Methods("POST")
	r.HandleFunc("/proxy/workflows-apiserver/{path:.*}", api.WorkflowApiserverProxy)
	r.HandleFunc("/proxy/svcname", api.GetSvcName).Queries("application", "").Methods("GET")

	r.Handle("/v2/apidocs.json", openAPI()).Methods("GET")

	return r
}

func (api *API) Serve(port int) {
	address := fmt.Sprintf(":%v", port)
	api.logger.Info("server started", zap.Int("port", port))
	err := http.ListenAndServe(address, api.GetHandler())
	api.logger.Fatal("done listening", zap.Error(err))
}
