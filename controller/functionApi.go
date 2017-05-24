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
	"encoding/base64"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/gorilla/mux"

	"github.com/fission/fission"
	"github.com/fission/fission/router"
)

func (api *API) FunctionApiList(w http.ResponseWriter, r *http.Request) {
	funcs, err := api.FunctionStore.List()
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(funcs)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	api.respondWithSuccess(w, resp)
}

func (api *API) FunctionApiCreate(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		api.respondWithError(w, err)
	}

	var f fission.Function
	err = json.Unmarshal(body, &f)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	dec, err := base64.StdEncoding.DecodeString(f.Code)
	if err != nil {
		api.respondWithError(w, err)
		return
	}
	f.Code = string(dec)

	uid, err := api.FunctionStore.Create(&f)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	m := &fission.Metadata{Name: f.Name, Uid: uid}
	resp, err := json.Marshal(m)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
	api.respondWithSuccess(w, resp)
}

func (api *API) FunctionApiGet(w http.ResponseWriter, r *http.Request) {
	var m fission.Metadata

	vars := mux.Vars(r)
	m.Name = vars["function"]
	m.Uid = r.FormValue("uid") // empty if uid is absent
	raw := r.FormValue("raw")  // just the code

	f, err := api.FunctionStore.Get(&m)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	var resp []byte
	if raw != "" {
		resp = []byte(f.Code)
	} else {
		f.Code = base64.StdEncoding.EncodeToString([]byte(f.Code))
		resp, err = json.Marshal(f)
		if err != nil {
			api.respondWithError(w, err)
			return
		}
	}
	api.respondWithSuccess(w, resp)
}

func (api *API) FunctionApiUpdate(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	funcName := vars["function"]

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		api.respondWithError(w, err)
	}

	var f fission.Function
	err = json.Unmarshal(body, &f)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	if funcName != f.Metadata.Name {
		err = fission.MakeError(fission.ErrorInvalidArgument, "Function name doesn't match URL")
		api.respondWithError(w, err)
		return
	}

	dec, err := base64.StdEncoding.DecodeString(f.Code)
	if err != nil {
		api.respondWithError(w, err)
		return
	}
	f.Code = string(dec)

	uid, err := api.FunctionStore.Update(&f)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	m := &fission.Metadata{Name: f.Name, Uid: uid}
	resp, err := json.Marshal(m)
	if err != nil {
		api.respondWithError(w, err)
		return
	}
	api.respondWithSuccess(w, resp)
}

func (api *API) FunctionApiDelete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var m fission.Metadata
	m.Name = vars["function"]

	m.Uid = r.FormValue("uid") // empty if uid is absent
	if len(m.Uid) == 0 {
		log.WithFields(log.Fields{"function": m.Name}).Info("Deleting all versions")
	}

	err := api.FunctionStore.Delete(m)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	api.respondWithSuccess(w, []byte(""))
}

// FunctionLogsApiPost establishes a proxy server to log database, and redirect
// query command send from client to database then proxy back the db response.
func (api *API) FunctionLogsApiPost(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	// get dbType from url
	dbType := vars["dbType"]

	// find correspond db http url
	dbCnf := api.getLogDBConfig(dbType)

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
	rrt := router.NewRetryingRoundTripper(10, 50*time.Millisecond)
	proxy := &httputil.ReverseProxy{
		Director:  director,
		Transport: rrt,
	}
	proxy.ServeHTTP(w, r)
}
