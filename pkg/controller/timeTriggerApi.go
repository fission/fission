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
	"github.com/robfig/cron"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
)

func RegisterTimeTriggerRoute(ws *restful.WebService) {
	tags := []string{"TimeTrigger"}
	specTag = append(specTag, spec.Tag{TagProps: spec.TagProps{Name: "TimeTrigger", Description: "TimeTrigger Operation"}})

	ws.Route(
		ws.GET("/v2/triggers/time").
			Doc("List all time trigger").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.QueryParameter("namespace", "Namespace of timeTrigger").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Writes([]fv1.TimeTrigger{}).
			Returns(http.StatusOK, "List of timeTriggers", []fv1.TimeTrigger{}))

	ws.Route(
		ws.POST("/v2/triggers/time").
			Doc("Create time trigger").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Produces(restful.MIME_JSON).
			Reads(fv1.TimeTrigger{}).
			Writes(metav1.ObjectMeta{}).
			Returns(http.StatusCreated, "ObjectMeta of created timeTrigger", metav1.ObjectMeta{}))

	ws.Route(
		ws.GET("/v2/triggers/time/{timeTrigger}").
			Doc("Get detail of time trigger").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("timeTrigger", "TimeTrigger name").DataType("string").DefaultValue("").Required(true)).
			Param(ws.QueryParameter("namespace", "Namespace of timeTrigger").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Writes(fv1.TimeTrigger{}). // on the response
			Returns(http.StatusOK, "A timeTrigger", fv1.TimeTrigger{}))

	ws.Route(
		ws.PUT("/v2/triggers/time/{timeTrigger}").
			Doc("Update time trigger").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("timeTrigger", "TimeTrigger name").DataType("string").DefaultValue("").Required(true)).
			Produces(restful.MIME_JSON).
			Reads(fv1.TimeTrigger{}).
			Writes(metav1.ObjectMeta{}). // on the response
			Returns(http.StatusOK, "ObjectMeta of updated timeTrigger", metav1.ObjectMeta{}))

	ws.Route(
		ws.DELETE("/v2/triggers/time/{timeTrigger}").
			Doc("Delete time trigger").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("timeTrigger", "TimeTrigger name").DataType("string").DefaultValue("").Required(true)).
			Param(ws.QueryParameter("namespace", "Namespace of timeTrigger").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Returns(http.StatusOK, "Only HTTP status returned", nil))
}

func (a *API) TimeTriggerApiList(w http.ResponseWriter, r *http.Request) {
	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceAll
	}

	triggers, err := a.fissionClient.CoreV1().TimeTriggers(ns).List(r.Context(), metav1.ListOptions{})
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

func (a *API) TimeTriggerApiCreate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	var t fv1.TimeTrigger
	err = json.Unmarshal(body, &t)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	// validate
	_, err = cron.Parse(t.Spec.Cron)
	if err != nil {
		err = ferror.MakeError(ferror.ErrorInvalidArgument, "TimeTrigger cron spec is not valid")
		a.respondWithError(w, err)
		return
	}

	// check if namespace exists, if not create it.
	err = a.createNsIfNotExists(r.Context(), t.ObjectMeta.Namespace)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	tnew, err := a.fissionClient.CoreV1().TimeTriggers(t.ObjectMeta.Namespace).Create(r.Context(), &t, metav1.CreateOptions{})
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

func (a *API) TimeTriggerApiGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["timeTrigger"]
	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceDefault
	}

	t, err := a.fissionClient.CoreV1().TimeTriggers(ns).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(t)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, resp)
}

func (a *API) TimeTriggerApiUpdate(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["timeTrigger"]

	body, err := io.ReadAll(r.Body)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	var t fv1.TimeTrigger
	err = json.Unmarshal(body, &t)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	if name != t.ObjectMeta.Name {
		err = ferror.MakeError(ferror.ErrorInvalidArgument, "TimeTrigger name doesn't match URL")
		a.respondWithError(w, err)
		return
	}

	_, err = cron.Parse(t.Spec.Cron)
	if err != nil {
		err = ferror.MakeError(ferror.ErrorInvalidArgument, "TimeTrigger cron spec is not valid")
		a.respondWithError(w, err)
		return
	}

	tnew, err := a.fissionClient.CoreV1().TimeTriggers(t.ObjectMeta.Namespace).Update(r.Context(), &t, metav1.UpdateOptions{})
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

func (a *API) TimeTriggerApiDelete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["timeTrigger"]
	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceDefault
	}

	err := a.fissionClient.CoreV1().TimeTriggers(ns).Delete(r.Context(), name, metav1.DeleteOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, []byte(""))
}
