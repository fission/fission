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

package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestProxyErrorHandler(t *testing.T) {
	config := zap.NewDevelopmentConfig()
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	logger, err := config.Build()

	assert.Nil(t, err)

	fh := &functionHandler{
		logger: logger,
		function: &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "dummy",
				Namespace: "dummy-bar",
			},
		},
	}

	errHandler := fh.getProxyErrorHandler(time.Now(), &RetryingRoundTripper{})

	req, err := http.NewRequest("GET", "http://foobar.com", nil)
	assert.Nil(t, err)

	req.Header.Set("foo", "bar")
	respRecorder := httptest.NewRecorder()
	errHandler(respRecorder, req, context.Canceled)
	assert.Equal(t, 499, respRecorder.Code)

	respRecorder = httptest.NewRecorder()
	errHandler(respRecorder, req, context.DeadlineExceeded)
	assert.Equal(t, http.StatusGatewayTimeout, respRecorder.Code)

	respRecorder = httptest.NewRecorder()
	errHandler(respRecorder, req, errors.New("dummy"))
	assert.Equal(t, http.StatusInternalServerError, respRecorder.Code)
}
