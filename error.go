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
)

func (e Error) Error() string {
	return fmt.Sprintf("(Error %v) %v", e.Code, e.Message)
}

func MakeError(code int, msg string) Error {
	return Error{Code: errorCode(code), Message: msg}
}

func (err Error) HTTPStatus() int {
	var code int
	switch err.Code {
	case ErrorNotFound:
		code = 404
	case ErrorInvalidArgument:
		code = 400
	case ErrorNoSpace:
		code = 500
	case ErrorNotAuthorized:
		code = 403
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
		msg = fe.Message
		code = fe.HTTPStatus()
	} else {
		code = 500
		msg = err.Error()
	}
	return code, msg
}
