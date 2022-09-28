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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"

	restfulspec "github.com/emicklei/go-restful-openapi/v2"
	restful "github.com/emicklei/go-restful/v3"
	"github.com/go-openapi/spec"
	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
)

func RegisterHTTPTriggerRoute(ws *restful.WebService) {
	tags := []string{"HTTPTrigger"}
	specTag = append(specTag, spec.Tag{TagProps: spec.TagProps{Name: "HTTPTrigger", Description: "HTTPTrigger Operation"}})

	ws.Route(
		ws.GET("/v2/triggers/http").
			Doc("List all HTTP triggers").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.QueryParameter("namespace", "Namespace of httpTrigger").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Writes([]fv1.HTTPTrigger{}).
			Returns(http.StatusOK, "List of httpTriggers", []fv1.HTTPTrigger{}))

	ws.Route(
		ws.POST("/v2/triggers/http").
			Doc("Create HTTP trigger").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Produces(restful.MIME_JSON).
			Reads(fv1.HTTPTrigger{}).
			Writes(metav1.ObjectMeta{}).
			Returns(http.StatusCreated, "ObjectMeta of created httpTrigger", metav1.ObjectMeta{}))

	ws.Route(
		ws.GET("/v2/triggers/http/{httpTrigger}").
			Doc("Get detail of HTTP trigger").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("httpTrigger", "HTTPTrigger name").DataType("string").DefaultValue("").Required(true)).
			Param(ws.QueryParameter("namespace", "Namespace of httpTrigger").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Writes(fv1.HTTPTrigger{}). // on the response
			Returns(http.StatusOK, "A httpTrigger", fv1.HTTPTrigger{}))

	ws.Route(
		ws.PUT("/v2/triggers/http/{httpTrigger}").
			Doc("Update HTTP trigger").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("httpTrigger", "HTTPTrigger name").DataType("string").DefaultValue("").Required(true)).
			Produces(restful.MIME_JSON).
			Reads(fv1.HTTPTrigger{}).
			Writes(metav1.ObjectMeta{}). // on the response
			Returns(http.StatusOK, "ObjectMeta of updated httpTrigger", metav1.ObjectMeta{}))

	ws.Route(
		ws.DELETE("/v2/triggers/http/{httpTrigger}").
			Doc("Delete HTTP trigger").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("httpTrigger", "HTTPTrigger name").DataType("string").DefaultValue("").Required(true)).
			Param(ws.QueryParameter("namespace", "Namespace of httpTrigger").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Returns(http.StatusOK, "Only HTTP status returned", nil))
}

func (a *API) HTTPTriggerApiList(w http.ResponseWriter, r *http.Request) {
	ns := a.extractQueryParamFromRequest(r, "namespace")
	// TODO: TBD do we want this behaviour
	if len(ns) == 0 {
		ns = metav1.NamespaceAll
	}

	triggers, err := a.fissionClient.CoreV1().HTTPTriggers(ns).List(r.Context(), metav1.ListOptions{})
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

// checkHTTPTriggerDuplicates checks whether the tuple (Method, Host, URL) is duplicate or not.
func (a *API) checkHTTPTriggerDuplicates(ctx context.Context, t *fv1.HTTPTrigger) error {
	triggers, err := a.fissionClient.CoreV1().HTTPTriggers(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, ht := range triggers.Items {
		if ht.ObjectMeta.UID == t.ObjectMeta.UID {
			// Same resource. No need to check.
			continue
		}
		urlMatch := false
		if (ht.Spec.RelativeURL != "" && ht.Spec.RelativeURL == t.Spec.RelativeURL) || (ht.Spec.Prefix != nil && t.Spec.Prefix != nil && *ht.Spec.Prefix != "" && *ht.Spec.Prefix == *t.Spec.Prefix) {
			urlMatch = true
		}
		methodMatch := false
		if ht.Spec.Method == t.Spec.Method && len(ht.Spec.Methods) == len(t.Spec.Methods) {
			methodMatch = true
			sort.Strings(ht.Spec.Methods)
			sort.Strings(t.Spec.Methods)
			for i, m1 := range ht.Spec.Methods {
				if m1 != t.Spec.Methods[i] {
					methodMatch = false
				}
			}
		}
		if urlMatch && methodMatch && ht.Spec.Method == t.Spec.Method && ht.Spec.Host == t.Spec.Host {
			return ferror.MakeError(ferror.ErrorNameExists,
				fmt.Sprintf("HTTPTrigger with same Host, URL & method already exists (%v)",
					ht.ObjectMeta.Name))
		}
	}
	return nil
}

func (a *API) HTTPTriggerApiCreate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	var t fv1.HTTPTrigger
	err = json.Unmarshal(body, &t)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	// Ensure we don't have a duplicate HTTP route defined (same URL and method)
	err = a.checkHTTPTriggerDuplicates(r.Context(), &t)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	// check if namespace exists, if not create it.
	err = a.createNsIfNotExists(r.Context(), t.ObjectMeta.Namespace)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	tnew, err := a.fissionClient.CoreV1().HTTPTriggers(t.ObjectMeta.Namespace).Create(r.Context(), &t, metav1.CreateOptions{})
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

func (a *API) HTTPTriggerApiGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["httpTrigger"]
	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceDefault
	}

	t, err := a.fissionClient.CoreV1().HTTPTriggers(ns).Get(r.Context(), name, metav1.GetOptions{})
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

func (a *API) HTTPTriggerApiUpdate(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["httpTrigger"]

	body, err := io.ReadAll(r.Body)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	var t fv1.HTTPTrigger
	err = json.Unmarshal(body, &t)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	if name != t.ObjectMeta.Name {
		err = ferror.MakeError(ferror.ErrorInvalidArgument, "HTTPTrigger name doesn't match URL")
		a.respondWithError(w, err)
		return
	}

	err = a.checkHTTPTriggerDuplicates(r.Context(), &t)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	tnew, err := a.fissionClient.CoreV1().HTTPTriggers(t.ObjectMeta.Namespace).Update(r.Context(), &t, metav1.UpdateOptions{})
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

func (a *API) HTTPTriggerApiDelete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["httpTrigger"]
	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceDefault
	}

	err := a.fissionClient.CoreV1().HTTPTriggers(ns).Delete(r.Context(), name, metav1.DeleteOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, []byte(""))
}
