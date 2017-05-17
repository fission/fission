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

package controller

import (
	"encoding/json"
	"io/ioutil"
	"net/http"

	log "github.com/Sirupsen/logrus"
	"github.com/gorilla/mux"
	"github.com/robfig/cron"

	"github.com/fission/fission"
)

func (api *API) TimeTriggerApiList(w http.ResponseWriter, r *http.Request) {
	triggers, err := api.TimeTriggerStore.List()
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

func (api *API) TimeTriggerApiCreate(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		api.respondWithError(w, err)
	}

	var t fission.TimeTrigger
	err = json.Unmarshal(body, &t)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	triggers, err := api.TimeTriggerStore.List()
	if err != nil {
		api.respondWithError(w, err)
		return
	}
	for _, trigger := range triggers {
		if trigger.Name == t.Name {
			err = fission.MakeError(fission.ErrorNameExists,
				"TimeTrigger with same name already exists")
			api.respondWithError(w, err)
			return
		}
	}

	_, err = cron.Parse(t.Cron)
	if err != nil {
		err = fission.MakeError(fission.ErrorInvalidArgument, "TimeTrigger cron spec is not valid")
		api.respondWithError(w, err)
		return
	}

	uid, err := api.TimeTriggerStore.Create(&t)
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

func (api *API) TimeTriggerApiGet(w http.ResponseWriter, r *http.Request) {
	var m fission.Metadata

	vars := mux.Vars(r)
	m.Name = vars["timeTrigger"]
	m.Uid = r.FormValue("uid") // empty if uid is absent

	t, err := api.TimeTriggerStore.Get(&m)
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

func (api *API) TimeTriggerApiUpdate(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["timeTrigger"]

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		api.respondWithError(w, err)
	}

	var t fission.TimeTrigger
	err = json.Unmarshal(body, &t)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	if name != t.Metadata.Name {
		err = fission.MakeError(fission.ErrorInvalidArgument, "TimeTrigger name doesn't match URL")
		api.respondWithError(w, err)
		return
	}

	_, err = cron.Parse(t.Cron)
	if err != nil {
		err = fission.MakeError(fission.ErrorInvalidArgument, "TimeTrigger cron spec is not valid")
		api.respondWithError(w, err)
		return
	}

	uid, err := api.TimeTriggerStore.Update(&t)
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

func (api *API) TimeTriggerApiDelete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var m fission.Metadata
	m.Name = vars["timeTrigger"]

	m.Uid = r.FormValue("uid") // empty if uid is absent
	if len(m.Uid) == 0 {
		log.WithFields(log.Fields{"timeTrigger": m.Name}).Info("Deleting all versions")
	}

	err := api.TimeTriggerStore.Delete(m)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	api.respondWithSuccess(w, []byte(""))
}
