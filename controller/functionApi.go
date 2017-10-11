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
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"

	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/1.5/pkg/api"
	"k8s.io/client-go/1.5/pkg/api/v1"
	"k8s.io/client-go/1.5/pkg/labels"
	"k8s.io/client-go/1.5/pkg/selection"
	"k8s.io/client-go/1.5/pkg/util/sets"

	"github.com/fission/fission"
	"github.com/fission/fission/tpr"
)

func (a *API) FunctionApiList(w http.ResponseWriter, r *http.Request) {
	funcs, err := a.fissionClient.Functions(api.NamespaceAll).List(api.ListOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(funcs.Items)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, resp)
}

func (a *API) FunctionApiCreate(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	var f tpr.Function
	err = json.Unmarshal(body, &f)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	err = validateResourceName(f.Metadata.Name)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	fnew, err := a.fissionClient.Functions(f.Metadata.Namespace).Create(&f)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(fnew.Metadata)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
	a.respondWithSuccess(w, resp)
}

func (a *API) FunctionApiGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["function"]
	ns := vars["namespace"]
	if len(ns) == 0 {
		ns = api.NamespaceDefault
	}

	f, err := a.fissionClient.Functions(ns).Get(name)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(f)
	if err != nil {
		a.respondWithError(w, err)
		return
	}
	a.respondWithSuccess(w, resp)
}

func (a *API) FunctionApiUpdate(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["function"]

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	var f tpr.Function
	err = json.Unmarshal(body, &f)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	if name != f.Metadata.Name {
		err = fission.MakeError(fission.ErrorInvalidArgument, "Function name doesn't match URL")
		a.respondWithError(w, err)
		return
	}

	fnew, err := a.fissionClient.Functions(f.Metadata.Namespace).Update(&f)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(fnew.Metadata)
	if err != nil {
		a.respondWithError(w, err)
		return
	}
	a.respondWithSuccess(w, resp)
}

func (a *API) FunctionApiDelete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["function"]
	ns := vars["namespace"]
	if len(ns) == 0 {
		ns = api.NamespaceDefault
	}

	err := a.fissionClient.Functions(ns).Delete(name, &api.DeleteOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, []byte(""))
}

// FunctionLogsApiPost establishes a proxy server to log database, and redirect
// query command send from client to database then proxy back the db response.
func (a *API) FunctionLogsApiPost(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	// get dbType from url
	dbType := vars["dbType"]

	// find correspond db http url
	dbCnf := a.getLogDBConfig(dbType)

	svcUrl, err := url.Parse(dbCnf.httpURL)
	if err != nil {
		log.Printf("Failed to establish proxy server for function logs: %v", err)
	}
	// set up proxy server director
	director := func(req *http.Request) {
		// only replace url Scheme and Host to remote influxDB
		// and leave query string intact
		req.URL.Scheme = svcUrl.Scheme
		req.URL.Host = svcUrl.Host
		req.URL.Path = svcUrl.Path
		// set up http basic auth for database authentication
		req.SetBasicAuth(dbCnf.username, dbCnf.password)
	}
	proxy := &httputil.ReverseProxy{
		Director: director,
	}
	proxy.ServeHTTP(w, r)
}

// FunctionPodLogs : Get logs for a function directly from pod
func (a *API) FunctionPodLogs(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	fnName := vars["function"]
	ns := vars["namespace"]

	if len(ns) == 0 {
		ns = "fission-function"
	}

	f, err := a.fissionClient.Functions(api.NamespaceDefault).Get(fnName)
	if err != nil {
		a.respondWithError(w, err)
		return
	}
	envName := f.Spec.Environment.Name

	_, clientset, err := tpr.GetKubernetesClient()
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	// Get Unmanaged Pods first
	nameFilter, _ := labels.NewRequirement("functionName", selection.Equals, sets.NewString(fnName))
	unmanagedFilter, _ := labels.NewRequirement("unmanaged", selection.Equals, sets.NewString("true"))
	selector := labels.NewSelector().Add(*nameFilter).Add(*unmanagedFilter)
	podList, err := clientset.Core().Pods(ns).List(api.ListOptions{LabelSelector: selector})

	// Get the logs for last Pod executed
	pods := podList.Items
	sort.Slice(pods, func(i, j int) bool {
		itime := pods[i].ObjectMeta.CreationTimestamp.Time
		jtime := pods[j].ObjectMeta.CreationTimestamp.Time
		return itime.After(jtime)
	})

	podLogOpts := v1.PodLogOptions{Container: envName} // Only the env container, not fetcher
	podLogsReq := clientset.Core().Pods(ns).GetLogs(pods[0].ObjectMeta.Name, &podLogOpts)
	podLogs, err := podLogsReq.Stream()
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	log, err := ioutil.ReadAll(podLogs)
	if err != nil {
		a.respondWithError(w, err)
		return
	}
	defer podLogs.Close()

	resp, err := json.Marshal(&log)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, resp)
}
