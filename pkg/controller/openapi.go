/*
Copyright 2019 The Fission Authors.

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

	"github.com/emicklei/go-restful"
	restfulspec "github.com/emicklei/go-restful-openapi"
	"github.com/go-openapi/spec"
)

var specTag []spec.Tag

func openAPI() http.Handler {
	restful.DefaultContainer.Add(openAPIWebService())

	config := restfulspec.Config{
		WebServices:                   restful.RegisteredWebServices(),
		APIPath:                       "/v2/apidocs.json",
		PostBuildSwaggerObjectHandler: enrichSwaggerObject}

	restful.DefaultContainer.Add(restfulspec.NewOpenAPIService(config))

	return restful.DefaultContainer
}

func openAPIWebService() *restful.WebService {
	ws := new(restful.WebService)

	// CRD resource
	// RegisterEnvironmentRoute(ws)
	RegisterFunctionRoute(ws)
	RegisterHTTPTriggerRoute(ws)
	RegisterMessageQueueTriggerRoute(ws)
	RegisterPackageRoute(ws)
	RegisterWatchRoute(ws)
	RegisterTimeTriggerRoute(ws)
	RegisterCanaryConfigRoute(ws)

	// proxy
	RegisterStorageServiceProxyRoute(ws)

	return ws
}

func enrichSwaggerObject(swo *spec.Swagger) {
	swo.Info = &spec.Info{
		InfoProps: spec.InfoProps{
			Title:       "Fission OpenAPI 2.0",
			Description: openapiDescription,
			Version:     "v1",
		},
	}
	swo.Tags = specTag
}

var openapiDescription = `
OpenAPI 2.0 document for fission controller
* ObjectMeta (v1.ObjectMeta) should be empty when creating a CRD resource. Kubernetes will assign it automatically.
* Following semantic errors are known issues and won't affect the API accessibility.
  - Operations must have unique operationIds.
  - All scale semantic errors. (Due to go-restful exposes inner fields of k8s struct).
`
