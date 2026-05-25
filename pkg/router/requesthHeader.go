// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net/http"

	"github.com/gorilla/mux"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// HEADERS_FISSION_FUNCTION_PREFIX represents a function prefix request header
	HEADERS_FISSION_FUNCTION_PREFIX = "Fission-Function"
)

// setFunctionMetadataToHeaders set function metadata to request header
func setFunctionMetadataToHeader(meta *metav1.ObjectMeta, request *http.Request) {
	request.Header.Set(fmt.Sprintf("X-%s-Uid", HEADERS_FISSION_FUNCTION_PREFIX), string(meta.UID))
	request.Header.Set(fmt.Sprintf("X-%s-Name", HEADERS_FISSION_FUNCTION_PREFIX), meta.Name)
	request.Header.Set(fmt.Sprintf("X-%s-Namespace", HEADERS_FISSION_FUNCTION_PREFIX), meta.Namespace)
	request.Header.Set(fmt.Sprintf("X-%s-ResourceVersion", HEADERS_FISSION_FUNCTION_PREFIX), meta.ResourceVersion)
}

// setPathInfoToHeaders set URL path params and full URL path to request header
func setPathInfoToHeader(request *http.Request) {
	// retrieve url params and add them to request header
	vars := mux.Vars(request)
	for k, v := range vars {
		request.Header.Set(fmt.Sprintf("X-Fission-Params-%v", k), v)
	}
	request.Header.Set("X-Fission-Full-Url", request.URL.String())
}
