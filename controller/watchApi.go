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

	log "github.com/Sirupsen/logrus"
	"github.com/gorilla/mux"

	"github.com/fission/fission"
)

func (api *API) WatchApiList(w http.ResponseWriter, r *http.Request) {
	watches, err := api.WatchStore.List()
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(watches)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	api.respondWithSuccess(w, resp)
}

func (api *API) WatchApiCreate(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		api.respondWithError(w, err)
	}

	var watch fission.Watch
	err = json.Unmarshal(body, &watch)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	watch.Target = fission.UrlForFunction(&watch.Function)

	uid, err := api.WatchStore.Create(&watch)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	m := &fission.Metadata{Name: watch.Metadata.Name, Uid: uid}
	resp, err := json.Marshal(m)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
	api.respondWithSuccess(w, resp)
}

func (api *API) WatchApiGet(w http.ResponseWriter, r *http.Request) {
	var m fission.Metadata

	vars := mux.Vars(r)
	m.Name = vars["watch"]
	m.Uid = r.FormValue("uid") // empty if uid is absent

	watch, err := api.WatchStore.Get(&m)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(watch)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	api.respondWithSuccess(w, resp)
}

func (api *API) WatchApiUpdate(w http.ResponseWriter, r *http.Request) {
	api.respondWithError(w, fission.MakeError(fission.ErrorNotImplmented,
		"Not implemented"))
}

func (api *API) WatchApiDelete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var m fission.Metadata
	m.Name = vars["watch"]

	m.Uid = r.FormValue("uid") // empty if uid is absent
	if len(m.Uid) == 0 {
		log.WithFields(log.Fields{"watch": m.Name}).Info("Deleting all versions")
	}

	err := api.WatchStore.Delete(m)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	api.respondWithSuccess(w, []byte(""))
}
