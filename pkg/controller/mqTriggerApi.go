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
	"io"
	"net/http"

	"github.com/emicklei/go-restful"
	restfulspec "github.com/emicklei/go-restful-openapi"
	"github.com/go-openapi/spec"
	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
)

func RegisterMessageQueueTriggerRoute(ws *restful.WebService) {
	tags := []string{"MessageQueueTrigger"}
	specTag = append(specTag, spec.Tag{TagProps: spec.TagProps{Name: "MessageQueueTrigger", Description: "MessageQueueTrigger Operation"}})

	ws.Route(
		ws.GET("/v2/triggers/messagequeue").
			Doc("List all message queue triggers").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.QueryParameter("namespace", "Namespace of messageQueueTrigger").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Writes([]fv1.MessageQueueTrigger{}).
			Returns(http.StatusOK, "List of messageQueueTriggers", []fv1.MessageQueueTrigger{}))

	ws.Route(
		ws.POST("/v2/triggers/messagequeue").
			Doc("Create message queue trigger").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Produces(restful.MIME_JSON).
			Reads(fv1.MessageQueueTrigger{}).
			Writes(metav1.ObjectMeta{}).
			Returns(http.StatusCreated, "ObjectMeta of created messageQueueTrigger", metav1.ObjectMeta{}))

	ws.Route(
		ws.GET("/v2/triggers/messagequeue/{mqTrigger}").
			Doc("Get detail of message queue trigger").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("mqTrigger", "MessageQueueTriggers name").DataType("string").DefaultValue("").Required(true)).
			Param(ws.QueryParameter("namespace", "Namespace of messageQueueTrigger").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Writes(fv1.MessageQueueTrigger{}). // on the response
			Returns(http.StatusOK, "A messageQueueTrigger", fv1.MessageQueueTrigger{}))

	ws.Route(
		ws.PUT("/v2/triggers/messagequeue/{mqTrigger}").
			Doc("Update message queue trigger").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("mqTrigger", "MessageQueueTrigger name").DataType("string").DefaultValue("").Required(true)).
			Produces(restful.MIME_JSON).
			Reads(fv1.MessageQueueTrigger{}).
			Writes(metav1.ObjectMeta{}). // on the response
			Returns(http.StatusOK, "ObjectMeta of updated messageQueueTrigger", metav1.ObjectMeta{}))

	ws.Route(
		ws.DELETE("/v2/triggers/messagequeue/{mqTrigger}").
			Doc("Delete message queue trigger").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("mqTrigger", "MessageQueueTrigger name").DataType("string").DefaultValue("").Required(true)).
			Param(ws.QueryParameter("namespace", "Namespace of messageQueueTrigger").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Returns(http.StatusOK, "Only HTTP status returned", nil))
}

func (a *API) MessageQueueTriggerApiList(w http.ResponseWriter, r *http.Request) {
	//mqType := r.FormValue("mqtype") // ignored for now
	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceAll
	}

	triggers, err := a.fissionClient.CoreV1().MessageQueueTriggers(ns).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}
	resp, err := json.Marshal(triggers.Items)
	if err != nil {
		a.respondWithError(w, err)
		return
	}
	a.respondWithSuccess(w, resp)
}

func (a *API) MessageQueueTriggerApiCreate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	var mqTrigger fv1.MessageQueueTrigger
	err = json.Unmarshal(body, &mqTrigger)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	// check if namespace exists, if not create it.
	err = a.createNsIfNotExists(r.Context(), mqTrigger.ObjectMeta.Namespace)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	tnew, err := a.fissionClient.CoreV1().MessageQueueTriggers(mqTrigger.ObjectMeta.Namespace).Create(r.Context(), &mqTrigger, metav1.CreateOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(tnew.ObjectMeta)
	if err != nil {
		a.respondWithError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	a.respondWithSuccess(w, resp)
}

func (a *API) MessageQueueTriggerApiGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["mqTrigger"]
	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceDefault
	}

	mqTrigger, err := a.fissionClient.CoreV1().MessageQueueTriggers(ns).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}
	resp, err := json.Marshal(mqTrigger)
	if err != nil {
		a.respondWithError(w, err)
		return
	}
	a.respondWithSuccess(w, resp)
}

func (a *API) MessageQueueTriggerApiUpdate(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["mqTrigger"]

	body, err := io.ReadAll(r.Body)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	var mqTrigger fv1.MessageQueueTrigger
	err = json.Unmarshal(body, &mqTrigger)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	if name != mqTrigger.ObjectMeta.Name {
		err = ferror.MakeError(ferror.ErrorInvalidArgument, "Message queue trigger name doesn't match URL")
		a.respondWithError(w, err)
		return
	}

	tnew, err := a.fissionClient.CoreV1().MessageQueueTriggers(mqTrigger.ObjectMeta.Namespace).Update(r.Context(), &mqTrigger, metav1.UpdateOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(tnew.ObjectMeta)
	if err != nil {
		a.respondWithError(w, err)
		return
	}
	a.respondWithSuccess(w, resp)
}

func (a *API) MessageQueueTriggerApiDelete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["mqTrigger"]
	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceDefault
	}

	err := a.fissionClient.CoreV1().MessageQueueTriggers(ns).Delete(r.Context(), name, metav1.DeleteOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}
	a.respondWithSuccess(w, []byte(""))
}
