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
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	apiv1 "k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/rest"
	k8sCache "k8s.io/client-go/tools/cache"

	"github.com/fission/fission"
	"github.com/fission/fission/crd"
	"github.com/fission/fission/fission/logdb"
)

type (
	API struct {
		fissionClient     *crd.FissionClient
		kubernetesClient  *kubernetes.Clientset
		storageServiceUrl string
		builderManagerUrl string
		workflowApiUrl    string
		functionNamespace string
		useIstio          bool
	}

	logDBConfig struct {
		httpURL  string
		username string
		password string
	}
)

func getIstioServiceLabels(fnName string) map[string]string {
	return map[string]string{
		"functionName": fnName,
	}
}

func makeFuncIstioServiceRegister(crdClient *rest.RESTClient,
	kubernetesClient *kubernetes.Clientset, fnNamespace string) k8sCache.Controller {

	resyncPeriod := 30 * time.Second
	lw := k8sCache.NewListWatchFromClient(crdClient, "functions", metav1.NamespaceDefault, fields.Everything())
	_, controller := k8sCache.NewInformer(lw, &crd.Function{}, resyncPeriod,
		k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {

				fn := obj.(*crd.Function)

				// Since istio only allows accessing pod through k8s service,
				// for the functions with executor type "poolmgr" we need to
				// create a service for sending requests to pod in pool.
				// Functions with executor type "Newdeploy" is specialized at
				// pod starts. In this case, just ignore such functions.
				fnExecutorType := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
				if fnExecutorType != fission.ExecutorTypePoolmgr {
					return
				}

				// create a same name service for function
				// since istio only allows the traffic to service
				sel := map[string]string{
					"functionName": fn.Metadata.Name,
					"functionUid":  string(fn.Metadata.UID),
				}

				svcName := fission.GetFunctionIstioServiceName(fn.Metadata.Name, fn.Metadata.Namespace)

				// service for accepting user traffic
				svc := apiv1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: fnNamespace,
						Name:      svcName,
						Labels:    getIstioServiceLabels(fn.Metadata.Name),
					},
					Spec: apiv1.ServiceSpec{
						Type: apiv1.ServiceTypeClusterIP,
						Ports: []apiv1.ServicePort{
							// Service port name should begin with a recognized prefix, or the traffic will be
							// treated as TCP traffic. (https://istio.io/docs/setup/kubernetes/sidecar-injection.html)
							// Originally the ports' name are similar to "http-fetch" and "http-specialize".
							// But for istio 0.5.1, istio-proxy return unexpected 431 error with such naming.
							// https://github.com/istio/istio/issues/928
							// Workaround: remove prefix
							// TODO: prepend prefix once the bug fixed
							{
								Name:       "fetch",
								Protocol:   apiv1.ProtocolTCP,
								Port:       8000,
								TargetPort: intstr.FromInt(8000),
							},
							{
								Name:       "specialize",
								Protocol:   apiv1.ProtocolTCP,
								Port:       8888,
								TargetPort: intstr.FromInt(8888),
							},
						},
						Selector: sel,
					},
				}

				// create function istio service if it does not exist
				_, err := kubernetesClient.CoreV1().Services(fnNamespace).Create(&svc)
				if err != nil && !kerrors.IsAlreadyExists(err) {
					log.Printf("Error creating function istio service: %v", err)
				}
			},
			DeleteFunc: func(obj interface{}) {
				fn := obj.(*crd.Function)
				svcName := fission.GetFunctionIstioServiceName(fn.Metadata.Name, fn.Metadata.Namespace)
				// delete function istio service
				err := kubernetesClient.CoreV1().Services(fnNamespace).Delete(svcName, nil)
				if err != nil && !kerrors.IsNotFound(err) {
					log.Printf("Error deleting function istio service: %v", err)
				}
			},
			UpdateFunc: func(oldObj, newObj interface{}) {},
		})

	return controller
}

func MakeAPI(ctx context.Context) (*API, error) {
	api, err := makeCRDBackedAPI()

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

	if len(os.Getenv("ENABLE_ISTIO")) > 0 {
		istio, err := strconv.ParseBool(os.Getenv("ENABLE_ISTIO"))
		if err != nil {
			log.Println("Failed to parse ENABLE_ISTIO")
		}
		api.useIstio = istio

		if api.useIstio {
			register := makeFuncIstioServiceRegister(
				api.fissionClient.GetCrdClient(), api.kubernetesClient, api.functionNamespace)
			register.Run(ctx.Done())
		}
	}

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
	debug.PrintStack()

	// this error type comes with an HTTP code, so just use that
	se, ok := err.(*kerrors.StatusError)
	if ok {
		http.Error(w, string(se.ErrStatus.Reason), int(se.ErrStatus.Code))
		return
	}

	code, msg := fission.GetHTTPError(err)
	log.Errorf("Error: %v: %v", code, msg)
	http.Error(w, msg, code)
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
	fmt.Fprintf(w, fission.VersionInfo().String())
}

func (api *API) ApiVersionMismatchHandler(w http.ResponseWriter, r *http.Request) {
	err := fission.MakeError(fission.ErrorNotFound, "Fission server supports API v2 only -- v1 is not supported. Please upgrade your Fission client/CLI.")
	api.respondWithError(w, err)
}

func (api *API) HealthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (api *API) Serve(port int) {
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

	r.HandleFunc("/v2/deleteTpr", api.Tpr2crdApi).Methods("DELETE")

	r.HandleFunc("/proxy/{dbType}", api.FunctionLogsApiPost).Methods("POST")
	r.HandleFunc("/proxy/storage/v1/archive", api.StorageServiceProxy)
	r.HandleFunc("/proxy/logs/{function}", api.FunctionPodLogs).Methods("POST")
	r.HandleFunc("/proxy/workflows-apiserver/{path:.*}", api.WorkflowApiserverProxy)

	address := fmt.Sprintf(":%v", port)

	log.WithFields(log.Fields{"port": port}).Info("Server started")
	r.Use(fission.LoggingMiddleware)
	log.Fatal(http.ListenAndServe(address, r))
}
