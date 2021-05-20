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
	"io/ioutil"
	"net/http"

	"github.com/emicklei/go-restful"
	restfulspec "github.com/emicklei/go-restful-openapi"
	"github.com/go-openapi/spec"
	"github.com/gorilla/mux"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	config "github.com/fission/fission/pkg/featureconfig"
)

func RegisterCanaryConfigRoute(ws *restful.WebService) {
	tags := []string{"CanaryConfig"}
	specTag = append(specTag, spec.Tag{TagProps: spec.TagProps{Name: "CanaryConfig", Description: "CanaryConfig Operation"}})

	ws.Route(
		ws.GET("/v2/canaryconfigs").
			Doc("List all canary configs").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.QueryParameter("namespace", "Namespace of canaryConfig").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Writes([]fv1.CanaryConfig{}).
			Returns(http.StatusOK, "List of canaryConfigs", []fv1.CanaryConfig{}))

	ws.Route(
		ws.POST("/v2/canaryconfigs").
			Doc("Create canary config").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Produces(restful.MIME_JSON).
			Reads(fv1.CanaryConfig{}).
			Writes(metav1.ObjectMeta{}).
			Returns(http.StatusCreated, "ObjectMeta of created canaryConfig", metav1.ObjectMeta{}))

	ws.Route(
		ws.GET("/v2/canaryconfigs/{canaryConfig}").
			Doc("Get detail of canary config").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("canaryConfig", "CanaryConfig name").DataType("string").DefaultValue("").Required(true)).
			Param(ws.QueryParameter("namespace", "Namespace of canaryConfig").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Writes(fv1.CanaryConfig{}). // on the response
			Returns(http.StatusOK, "A canaryConfig", fv1.CanaryConfig{}))

	ws.Route(
		ws.PUT("/v2/canaryconfigs/{canaryConfig}").
			Doc("Update canary config").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("canaryConfig", "CanaryConfig name").DataType("string").DefaultValue("").Required(true)).
			Produces(restful.MIME_JSON).
			Reads(fv1.CanaryConfig{}).
			Writes(metav1.ObjectMeta{}). // on the response
			Returns(http.StatusOK, "ObjectMeta of updated canaryConfig", metav1.ObjectMeta{}))

	ws.Route(
		ws.DELETE("/v2/canaryconfigs/{canaryConfig}").
			Doc("Delete canary config").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("canaryConfig", "CanaryConfig name").DataType("string").DefaultValue("").Required(true)).
			Param(ws.QueryParameter("namespace", "Namespace of canaryConfig").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Returns(http.StatusOK, "Only HTTP status returned", nil))
}

func (a *API) CanaryConfigApiCreate(w http.ResponseWriter, r *http.Request) {
	featureErr := a.featureStatus[config.CanaryFeature]
	if len(featureErr) > 0 {
		a.respondWithError(w, ferror.MakeError(http.StatusInternalServerError, fmt.Sprintf("Error enabling canary feature: %v", featureErr)))
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	var canaryCfg fv1.CanaryConfig
	err = json.Unmarshal(body, &canaryCfg)
	if err != nil {
		a.logger.Error("failed to unmarshal request body", zap.Error(err), zap.Binary("body", body))
		a.respondWithError(w, err)
		return
	}

	canaryCfgNew, err := a.fissionClient.CoreV1().CanaryConfigs(canaryCfg.ObjectMeta.Namespace).Create(context.Background(), &canaryCfg, metav1.CreateOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(canaryCfgNew.ObjectMeta)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
	a.respondWithSuccess(w, resp)
}

func (a *API) CanaryConfigApiGet(w http.ResponseWriter, r *http.Request) {
	featureErr := a.featureStatus[config.CanaryFeature]
	if len(featureErr) > 0 {
		a.respondWithError(w, ferror.MakeError(http.StatusInternalServerError, fmt.Sprintf("Error enabling canary feature: %v", featureErr)))
		return
	}

	vars := mux.Vars(r)
	name := vars["canaryConfig"]

	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceDefault
	}

	canaryCfg, err := a.fissionClient.CoreV1().CanaryConfigs(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(canaryCfg)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, resp)
}

func (a *API) CanaryConfigApiList(w http.ResponseWriter, r *http.Request) {
	featureErr := a.featureStatus[config.CanaryFeature]
	if len(featureErr) > 0 {
		a.respondWithError(w, ferror.MakeError(http.StatusInternalServerError, fmt.Sprintf("Error enabling canary feature: %v", featureErr)))
		return
	}

	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceDefault
	}

	canaryCfgs, err := a.fissionClient.CoreV1().CanaryConfigs(ns).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(canaryCfgs.Items)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, resp)
}

func (a *API) CanaryConfigApiUpdate(w http.ResponseWriter, r *http.Request) {
	featureErr := a.featureStatus[config.CanaryFeature]
	if len(featureErr) > 0 {
		a.respondWithError(w, ferror.MakeError(http.StatusInternalServerError, fmt.Sprintf("Error enabling canary feature: %v", featureErr)))
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	var c fv1.CanaryConfig
	err = json.Unmarshal(body, &c)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	canayCfgNew, err := a.fissionClient.CoreV1().CanaryConfigs(c.ObjectMeta.Namespace).Update(context.Background(), &c, metav1.UpdateOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(canayCfgNew.ObjectMeta)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, resp)
}

func (a *API) CanaryConfigApiDelete(w http.ResponseWriter, r *http.Request) {
	featureErr := a.featureStatus[config.CanaryFeature]
	if len(featureErr) > 0 {
		a.respondWithError(w, ferror.MakeError(http.StatusInternalServerError, fmt.Sprintf("Error enabling canary feature: %v", featureErr)))
		return
	}

	vars := mux.Vars(r)
	name := vars["canaryConfig"]
	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceDefault
	}

	err := a.fissionClient.CoreV1().CanaryConfigs(ns).Delete(context.Background(), name, metav1.DeleteOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, []byte(""))
}
