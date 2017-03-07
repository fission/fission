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

package fission

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
)

func (err Error) Error() string {
	return fmt.Sprintf("%v - %v", err.Description(), err.Message)
}

func MakeError(code int, msg string) Error {
	return Error{Code: errorCode(code), Message: msg}
}

func MakeErrorFromHTTP(resp *http.Response) error {
	if resp.StatusCode == 200 {
		return nil
	}

	var errCode int
	switch resp.StatusCode {
	case 400:
		errCode = ErrorInvalidArgument
	case 403:
		errCode = ErrorNotAuthorized
	case 404:
		errCode = ErrorNotFound
	case 409:
		errCode = ErrorNameExists
	default:
		errCode = ErrorInternal
	}

	msg := resp.Status
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err == nil && len(body) > 0 {
		msg = strings.TrimSpace(string(body))
	}

	return MakeError(errCode, msg)
}

func (err Error) HTTPStatus() int {
	var code int
	switch err.Code {
	case ErrorInvalidArgument:
		code = 400
	case ErrorNotAuthorized:
		code = 403
	case ErrorNotFound:
		code = 404
	case ErrorNameExists:
		code = 409
	default:
		code = 500
	}
	return code
}

func GetHTTPError(err error) (int, string) {
	var msg string
	var code int
	fe, ok := err.(Error)
	if ok {
		code = fe.HTTPStatus()
		msg = fe.Message
	} else {
		code = 500
		msg = err.Error()
	}
	return code, msg
}

func (err Error) Description() string {
	idx := int(err.Code)
	if idx < 0 || idx > len(errorDescriptions)-1 {
		return ""
	}
	return errorDescriptions[idx]
}
