// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/utils/httpmux"
)

const (
	// HEADERS_FISSION_FUNCTION_PREFIX represents a function prefix request header
	HEADERS_FISSION_FUNCTION_PREFIX = "Fission-Function"

	// Per-request function-metadata header names. Kept as constants (rather than
	// rebuilt with fmt.Sprintf on every request) since they derive from the
	// compile-time HEADERS_FISSION_FUNCTION_PREFIX above.
	headerFissionFunctionUID             = "X-" + HEADERS_FISSION_FUNCTION_PREFIX + "-Uid"
	headerFissionFunctionName            = "X-" + HEADERS_FISSION_FUNCTION_PREFIX + "-Name"
	headerFissionFunctionNamespace       = "X-" + HEADERS_FISSION_FUNCTION_PREFIX + "-Namespace"
	headerFissionFunctionResourceVersion = "X-" + HEADERS_FISSION_FUNCTION_PREFIX + "-ResourceVersion"
)

// setFunctionMetadataToHeaders set function metadata to request header
func setFunctionMetadataToHeader(meta *metav1.ObjectMeta, request *http.Request) {
	request.Header.Set(headerFissionFunctionUID, string(meta.UID))
	request.Header.Set(headerFissionFunctionName, meta.Name)
	request.Header.Set(headerFissionFunctionNamespace, meta.Namespace)
	request.Header.Set(headerFissionFunctionResourceVersion, meta.ResourceVersion)
}

// setPathInfoToHeaders set URL path params and full URL path to request header
func setPathInfoToHeader(request *http.Request) {
	// retrieve url params and add them to request header
	vars := httpmux.Vars(request)
	for k, v := range vars {
		request.Header.Set(fmt.Sprintf("X-Fission-Params-%v", k), v)
	}
	request.Header.Set("X-Fission-Full-Url", request.URL.String())
}
