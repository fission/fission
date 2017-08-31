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
	"net/http"
	"net/http/httputil"
	"net/url"
)

func (api *API) StorageServiceProxy(w http.ResponseWriter, r *http.Request) {
	u := api.storageServiceUrl + "/v1/archive"
	proxy := httputil.NewSingleHostReverseProxy(url.Parse(u))
	proxy.ServeHTTP(w, r)
}
