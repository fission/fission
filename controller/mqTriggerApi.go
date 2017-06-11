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

	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"

	"github.com/fission/fission"
)

func (api *API) MessageQueueTriggerApiList(w http.ResponseWriter, r *http.Request) {
	mqType := r.FormValue("mqtype")
	triggers, err := api.MessageQueueTriggerStore.List(mqType)
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

func (api *API) MessageQueueApiCreate(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	var mqTrigger fission.MessageQueueTrigger
	err = json.Unmarshal(body, &mqTrigger)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	// trigger name must not conflict with any other trigger
	// even they are different message queue type
	triggers, err := api.MessageQueueTriggerStore.List("")
	if err != nil {
		api.respondWithError(w, err)
		return
	}
	for _, trigger := range triggers {
		if trigger.Name == mqTrigger.Name {
			err = fission.MakeError(fission.ErrorNameExists,
				"MessageQueueTrigger with same name already exists")
			api.respondWithError(w, err)
			return
		}
	}

	// save trigger info
	uid, err := api.MessageQueueTriggerStore.Create(&mqTrigger)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	mqTriggerMeta := fission.Metadata{Name: mqTrigger.Metadata.Name, Uid: uid}
	resp, err := json.Marshal(mqTriggerMeta)
	if err != nil {
		api.respondWithError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	api.respondWithSuccess(w, resp)
}

func (api *API) MessageQueueApiGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	mqTriggerMeta := fission.Metadata{
		Name: vars["mqTrigger"],
		Uid:  r.FormValue("uid"), // empty if uid is absent
	}
	mqTrigger, err := api.MessageQueueTriggerStore.Get(&mqTriggerMeta)
	if err != nil {
		api.respondWithError(w, err)
		return
	}
	resp, err := json.Marshal(mqTrigger)
	if err != nil {
		api.respondWithError(w, err)
		return
	}
	api.respondWithSuccess(w, resp)
}

func (api *API) MessageQueueApiUpdate(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	mqtName := vars["mqTrigger"]

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	var mqTrigger fission.MessageQueueTrigger
	err = json.Unmarshal(body, &mqTrigger)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	if mqtName != mqTrigger.Metadata.Name {
		err = fission.MakeError(fission.ErrorInvalidArgument, "MessageQueueTrigger name doesn't match URL")
		api.respondWithError(w, err)
		return
	}

	uid, err := api.MessageQueueTriggerStore.Update(&mqTrigger)
	if err != nil {
		api.respondWithError(w, err)
		return
	}

	mqTriggerMeta := fission.Metadata{Name: mqTrigger.Metadata.Name, Uid: uid}
	resp, err := json.Marshal(mqTriggerMeta)
	if err != nil {
		api.respondWithError(w, err)
		return
	}
	api.respondWithSuccess(w, resp)
}

func (api *API) MessageQueueApiDelete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	mqTriggerMeta := fission.Metadata{
		Name: vars["mqTrigger"],
		Uid:  r.FormValue("uid"), // empty if uid is absent
	}

	if len(mqTriggerMeta.Uid) == 0 {
		log.WithFields(log.Fields{"mqTrigger": mqTriggerMeta.Name}).Info("Deleting all versions")
	}

	err := api.MessageQueueTriggerStore.Delete(mqTriggerMeta)
	if err != nil {
		api.respondWithError(w, err)
		return
	}
	api.respondWithSuccess(w, []byte(""))
}
