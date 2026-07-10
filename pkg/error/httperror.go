// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package error

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type (
	// Errors returned by the Fission API.
	Error struct {
		Code    errorCode `json:"code"`
		Message string    `json:"message"`
	}

	errorCode int
)

func (err Error) Error() string {
	return fmt.Sprintf("%v - %v", err.Description(), err.Message)
}

func MakeError(code int, msg string) Error {
	return Error{Code: errorCode(code), Message: msg}
}

func MakeErrorFromHTTP(resp *http.Response) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}

	var errCode int
	switch resp.StatusCode {
	case http.StatusBadRequest:
		errCode = ErrorInvalidArgument
	case http.StatusForbidden:
		errCode = ErrorNotAuthorized
	case http.StatusNotFound:
		errCode = ErrorNotFound
	case http.StatusConflict:
		errCode = ErrorNameExists
	case http.StatusRequestTimeout:
		errCode = ErrorRequestTimeout
	case http.StatusTooManyRequests:
		errCode = ErrorTooManyRequests
	case http.StatusUnauthorized:
		errCode = ErrorNotAuthorized
	default:
		errCode = ErrorInternal
	}

	msg := resp.Status
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err == nil && len(body) > 0 {
		msg = strings.TrimSpace(string(body))
	}

	return MakeError(errCode, msg)
}

func (err Error) HTTPStatus() int {
	var code int
	switch err.Code {
	case ErrorInvalidArgument:
		code = http.StatusBadRequest
	case ErrorNotAuthorized:
		code = http.StatusForbidden
	case ErrorNotFound:
		code = http.StatusNotFound
	case ErrorNameExists:
		code = http.StatusConflict
	case ErrorTooManyRequests:
		code = http.StatusTooManyRequests
	default:
		code = http.StatusInternalServerError
	}
	return code
}

func (err Error) Description() string {
	idx := int(err.Code)
	if idx < 0 || idx > len(errorDescriptions)-1 {
		return ""
	}
	return errorDescriptions[idx]
}

func GetHTTPError(err error) (int, string) {
	var msg string
	var code int
	var fe Error
	if errors.As(err, &fe) {
		code = fe.HTTPStatus()
		msg = fe.Message
	} else {
		code = http.StatusInternalServerError
		msg = err.Error()
	}
	return code, msg
}

func IsNotFound(err error) bool {
	var fe Error
	if !errors.As(err, &fe) {
		return false
	}

	return fe.Code == ErrorNotFound
}

// IsTooManyRequests reports whether err is a Fission capacity rejection
// (surfaced to clients as HTTP 429).
func IsTooManyRequests(err error) bool {
	var fe Error
	if !errors.As(err, &fe) {
		return false
	}

	return fe.Code == ErrorTooManyRequests
}

const (
	ErrorInternal = iota

	ErrorNotAuthorized
	ErrorNotFound
	ErrorNameExists
	ErrorInvalidArgument
	ErrorNoSpace
	ErrorNotImplemented
	ErrorChecksumFail
	ErrorSizeLimitExceeded
	ErrorRequestTimeout
	ErrorTooManyRequests
)

// must match order and len of the above const
var errorDescriptions = []string{
	"Internal error",
	"Not authorized",
	"Resource not found",
	"Resource exists",
	"Invalid argument",
	"No space",
	"Not implemented",
	"Checksum verification failed",
	"Size limit exceeded",
	"Request time limit exceeded",
}
