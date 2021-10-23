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
	"fmt"
	"io"
	"net/http"

	"github.com/dustin/go-humanize"
	"github.com/emicklei/go-restful"
	restfulspec "github.com/emicklei/go-restful-openapi"
	"github.com/go-openapi/spec"
	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
)

func RegisterPackageRoute(ws *restful.WebService) {
	tags := []string{"Package"}
	specTag = append(specTag, spec.Tag{TagProps: spec.TagProps{Name: "Package", Description: "Package Operation"}})

	ws.Route(
		ws.GET("/v2/packages").
			Doc("List all packages").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.QueryParameter("namespace", "Namespace of package").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Writes([]fv1.Package{}).
			Returns(http.StatusOK, "List of packages", []fv1.Package{}))

	ws.Route(
		ws.POST("/v2/packages").
			Doc("Create package").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Produces(restful.MIME_JSON).
			Reads(fv1.Package{}).
			Writes(metav1.ObjectMeta{}).
			Returns(http.StatusCreated, "ObjectMeta of created package", metav1.ObjectMeta{}))

	ws.Route(
		ws.GET("/v2/packages/{package}").
			Doc("Get detail of package").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("package", "Package name").DataType("string").DefaultValue("").Required(true)).
			Param(ws.QueryParameter("namespace", "Namespace of package").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Writes(fv1.Package{}). // on the response
			Returns(http.StatusOK, "A package", fv1.Package{}))

	ws.Route(
		ws.PUT("/v2/packages/{package}").
			Doc("Update package").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("package", "Package name").DataType("string").DefaultValue("").Required(true)).
			Produces(restful.MIME_JSON).
			Reads(fv1.Package{}).
			Writes(metav1.ObjectMeta{}). // on the response
			Returns(http.StatusOK, "ObjectMeta of updated package", metav1.ObjectMeta{}))

	ws.Route(
		ws.DELETE("/v2/packages/{package}").
			Doc("Delete package").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}).
			Param(ws.PathParameter("package", "Package name").DataType("string").DefaultValue("").Required(true)).
			Param(ws.QueryParameter("namespace", "Namespace of package").DataType("string").DefaultValue(metav1.NamespaceAll).Required(false)).
			Produces(restful.MIME_JSON).
			Returns(http.StatusOK, "Only HTTP status returned", nil))
}

func (a *API) PackageApiList(w http.ResponseWriter, r *http.Request) {
	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceAll
	}
	funcs, err := a.fissionClient.CoreV1().Packages(ns).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(funcs.Items)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, resp)
}

func (a *API) PackageApiCreate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	var f fv1.Package
	err = json.Unmarshal(body, &f)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	// Ensure size limits
	if len(f.Spec.Source.Literal) > int(fv1.ArchiveLiteralSizeLimit) {
		err := ferror.MakeError(ferror.ErrorInvalidArgument,
			fmt.Sprintf("Package literal larger than %s", humanize.Bytes(uint64(fv1.ArchiveLiteralSizeLimit))))
		a.respondWithError(w, err)
		return
	}
	if len(f.Spec.Deployment.Literal) > int(fv1.ArchiveLiteralSizeLimit) {
		err := ferror.MakeError(ferror.ErrorInvalidArgument,
			fmt.Sprintf("Package literal larger than %s", humanize.Bytes(uint64(fv1.ArchiveLiteralSizeLimit))))
		a.respondWithError(w, err)
		return
	}

	// check if namespace exists, if not create it.
	err = a.createNsIfNotExists(r.Context(), f.ObjectMeta.Namespace)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	fnew, err := a.fissionClient.CoreV1().Packages(f.ObjectMeta.Namespace).Create(r.Context(), &f, metav1.CreateOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(fnew.ObjectMeta)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
	a.respondWithSuccess(w, resp)
}

func (a *API) PackageApiGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["package"]
	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceDefault
	}
	raw := r.FormValue("raw") // just the deployment pkg

	f, err := a.fissionClient.CoreV1().Packages(ns).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	var resp []byte
	if raw != "" {
		resp = f.Spec.Deployment.Literal
	} else {
		resp, err = json.Marshal(f)
		if err != nil {
			a.respondWithError(w, err)
			return
		}
	}
	a.respondWithSuccess(w, resp)
}

func (a *API) PackageApiUpdate(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["package"]

	body, err := io.ReadAll(r.Body)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	var f fv1.Package
	err = json.Unmarshal(body, &f)
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	if name != f.ObjectMeta.Name {
		err = ferror.MakeError(ferror.ErrorInvalidArgument, "Package name doesn't match URL")
		a.respondWithError(w, err)
		return
	}

	fnew, err := a.fissionClient.CoreV1().Packages(f.ObjectMeta.Namespace).Update(r.Context(), &f, metav1.UpdateOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	resp, err := json.Marshal(fnew.ObjectMeta)
	if err != nil {
		a.respondWithError(w, err)
		return
	}
	a.respondWithSuccess(w, resp)
}

func (a *API) PackageApiDelete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["package"]
	ns := a.extractQueryParamFromRequest(r, "namespace")
	if len(ns) == 0 {
		ns = metav1.NamespaceDefault
	}

	err := a.fissionClient.CoreV1().Packages(ns).Delete(r.Context(), name, metav1.DeleteOptions{})
	if err != nil {
		a.respondWithError(w, err)
		return
	}

	a.respondWithSuccess(w, []byte(""))
}
