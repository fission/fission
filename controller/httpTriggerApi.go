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

func (api *API) HTTPTriggerApiList(w http.ResponseWriter, r *http.Request) {
	triggers, err := api.HTTPTriggerStore.List()
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(triggers)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	api.respondWithSuccess(w, resp)
}

func (api *API) HTTPTriggerApiCreate(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		api.respondWithError(w, err)
	}

	var t fission.HTTPTrigger
	err = json.Unmarshal(body, &t)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	triggers, err := api.HTTPTriggerStore.List()
	if err != nil {
		api.respondWithError(w, err)
		return
	}
	for _, url := range triggers {
		if url.UrlPattern == t.UrlPattern && url.Method == t.Method {
			err = fission.MakeError(fission.ErrorNameExists,
				"HTTPTrigger with same URL & method already exists")
			api.respondWithError(w, err)
			return
		}
	}

	uid, err := api.HTTPTriggerStore.Create(&t)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	m := &fission.Metadata{Name: t.Metadata.Name, Uid: uid}
	resp, err := json.Marshal(m)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
	api.respondWithSuccess(w, resp)
}

func (api *API) HTTPTriggerApiGet(w http.ResponseWriter, r *http.Request) {
	var m fission.Metadata

	vars := mux.Vars(r)
	m.Name = vars["httpTrigger"]
	m.Uid = r.FormValue("uid") // empty if uid is absent

	t, err := api.HTTPTriggerStore.Get(&m)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(t)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	api.respondWithSuccess(w, resp)
}

func (api *API) HTTPTriggerApiUpdate(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["httpTrigger"]

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		api.respondWithError(w, err)
	}

	var t fission.HTTPTrigger
	err = json.Unmarshal(body, &t)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	if name != t.Metadata.Name {
		err = fission.MakeError(fission.ErrorInvalidArgument, "HTTPTrigger name doesn't match URL")
		api.respondWithError(w, err)
		return
	}

	uid, err := api.HTTPTriggerStore.Update(&t)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	m := &fission.Metadata{Name: t.Metadata.Name, Uid: uid}
	resp, err := json.Marshal(m)
	if err != nil {
		api.respondWithError(w, err)
		return
	}
	api.respondWithSuccess(w, resp)
}

func (api *API) HTTPTriggerApiDelete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var m fission.Metadata
	m.Name = vars["httpTrigger"]

	m.Uid = r.FormValue("uid") // empty if uid is absent
	if len(m.Uid) == 0 {
		log.WithFields(log.Fields{"httpTrigger": m.Name}).Info("Deleting all versions")
	}

	err := api.HTTPTriggerStore.Delete(m)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	api.respondWithSuccess(w, []byte(""))
}
