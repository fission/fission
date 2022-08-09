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
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"

	restfulspec "github.com/emicklei/go-restful-openapi/v2"
	restful "github.com/emicklei/go-restful/v3"
	"github.com/go-openapi/spec"
	"go.uber.org/zap"
)

func RegisterStorageServiceProxyRoute(ws *restful.WebService) {
	tags := []string{"StorageServiceProxy"}
	specTag = append(specTag, spec.Tag{TagProps: spec.TagProps{Name: "StorageServiceProxy", Description: "StorageServiceProxy Operation"}})

	// workaround as go-restful has to set HTTP method explicitly.
	ws.Route(
		ws.POST("/proxy/storage/v1/archive").
			Doc("Create archive").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}))
	ws.Route(
		ws.GET("/proxy/storage/v1/archive").
			Doc("Get archive").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}))
	ws.Route(
		ws.DELETE("/proxy/storage/v1/archive").
			Doc("Delete archive").
			Metadata(restfulspec.KeyOpenAPITags, tags).
			To(func(req *restful.Request, resp *restful.Response) {
				resp.ResponseWriter.WriteHeader(http.StatusOK)
			}))
}

func (api *API) StorageServiceProxy(w http.ResponseWriter, r *http.Request) {
	u := api.storageServiceUrl
	ssUrl, err := url.Parse(u)
	if err != nil {
		e := "error parsing url"
		api.logger.Error(e, zap.Error(err), zap.String("url", u))
		http.Error(w, fmt.Sprintf("%s %s: %v", e, u, err), http.StatusInternalServerError)
		return
	}
	director := func(req *http.Request) {
		req.URL.Scheme = ssUrl.Scheme
		req.URL.Host = ssUrl.Host
		req.URL.Path = "/v1/archive"
		req.Host = ssUrl.Host
	}
	proxy := &httputil.ReverseProxy{
		Director: director,
	}
	proxy.ServeHTTP(w, r)
}
