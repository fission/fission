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

	"github.com/emicklei/go-restful"
	restfulspec "github.com/emicklei/go-restful-openapi"
	"github.com/go-openapi/spec"
	"github.com/gorilla/mux"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	v1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
)

func RegisterEnvironmentRoute(ws *restful.WebService) {
	tags := []string{"Environment"}
	specTag = append(specTag, spec.Tag{TagProps: spec.TagProps{Name: "Environment", Description: "Environment Operation"}})

	ws.Route(
		ws.GET("/v2/environments").
			Doc("List all environments").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.QueryParameter("namespace", "Namespace of environment").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Writes([]fv1.Environment{}).
			Returns(http.StatusOK, "List of environments", []fv1.Environment{}))

	ws.Route(
		ws.POST("/v2/environments").
			Doc("Create environment").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Produces(restful.MIME_JSON).
			Reads(fv1.Environment{}).
			Writes(metav1.ObjectMeta{}).
			Returns(http.StatusCreated, "ObjectMeta of created environment", metav1.ObjectMeta{}))

	ws.Route(
		ws.GET("/v2/environments/{environment}").
			Doc("Get detail of environment").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("environment", "Environment name").DataType("string").DefaultValue("").Required(true)).
			Param(ws.QueryParameter("namespace", "Namespace of environment").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Writes(fv1.Environment{}). // on the response
			Returns(http.StatusOK, "A environment", fv1.Environment{}))

	ws.Route(
		ws.PUT("/v2/environments/{environment}").
			Doc("Update environment").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("environment", "Environment name").DataType("string").DefaultValue("").Required(true)).
			Produces(restful.MIME_JSON).
			Reads(fv1.Environment{}).
			Writes(metav1.ObjectMeta{}). // on the response
			Returns(http.StatusOK, "ObjectMeta of updated environment", metav1.ObjectMeta{}))

	ws.Route(
		ws.DELETE("/v2/environments/{environment}").
			Doc("Delete environment").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("environment", "Environment name").DataType("string").DefaultValue("").Required(true)).
			Param(ws.QueryParameter("namespace", "Namespace of environment").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Returns(http.StatusOK, "Only HTTP status returned", nil))
}

func (a *API) EnvironmentApiList(w http.ResponseWriter, r *http.Request) {
	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceAll
	}

	envs, err := a.fissionClient.CoreV1().Environments(ns).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(envs.Items)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, resp)
}

func (a *API) EnvironmentApiCreate(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	var env fv1.Environment
	err = json.Unmarshal(body, &env)
	if err != nil {
		a.logger.Error("failed to unmarshal request body", zap.Error(err), zap.Binary("body", body))
		a.respondWithError(w, err)
		return
	}

	// check if namespace exists, if not create it.
	err = a.createNsIfNotExists(r.Context(), env.ObjectMeta.Namespace)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	enew, err := a.fissionClient.CoreV1().Environments(env.ObjectMeta.Namespace).Create(r.Context(), &env, metav1.CreateOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(enew.ObjectMeta)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
	a.respondWithSuccess(w, resp)
}

func (a *API) EnvironmentApiGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["environment"]

	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceDefault
	}

	env, err := a.fissionClient.CoreV1().Environments(ns).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(env)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, resp)
}

func (a *API) EnvironmentApiUpdate(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["environment"]

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	var env fv1.Environment
	err = json.Unmarshal(body, &env)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	if name != env.ObjectMeta.Name {
		err = ferror.MakeError(ferror.ErrorInvalidArgument, "Environment name doesn't match URL")
		a.respondWithError(w, err)
		return
	}

	enew, err := a.fissionClient.CoreV1().Environments(env.ObjectMeta.Namespace).Update(r.Context(), &env, metav1.UpdateOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(enew.ObjectMeta)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, resp)
}

func (a *API) EnvironmentApiDelete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["environment"]

	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceDefault
	}

	err := a.fissionClient.CoreV1().Environments(ns).Delete(r.Context(), name, metav1.DeleteOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, []byte(""))
}

// EnvironmentApiPodList: Get list of pods currently available in environment
func (a *API) EnvironmentApiPodList(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	envName, ok := vars["environment"]
	if !ok {
		a.respondWithError(w, ferror.MakeError(http.StatusInternalServerError, "Error retrieving environment name"))
		return
	}

	// label selector
	selector := map[string]string{
		fv1.ENVIRONMENT_NAME: envName,
	}

	ens := a.extractQueryParamFromRequest(r, v1.ENVIRONMENT_NAMESPACE)
	if len(ens) != 0 {
		selector[fv1.ENVIRONMENT_NAMESPACE] = ens
	}

	et := a.extractQueryParamFromRequest(r, v1.EXECUTOR_TYPE)
	if len(et) != 0 {
		selector[fv1.EXECUTOR_TYPE] = et
	}

	pods, err := a.kubernetesClient.CoreV1().Pods(metav1.NamespaceAll).List(r.Context(), metav1.ListOptions{
		LabelSelector: labels.Set(selector).AsSelector().String(),
	})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(pods.Items)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, resp)
}
