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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func Test_withHTTPTriggerSet(t *testing.T) {
	config := zap.NewDevelopmentConfig()
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	logger, err := config.Build()
	assert.Nil(t, err)

	ts := &HTTPTriggerSet{
		logger: logger,
		functions: []fv1.Function{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "dummy",
					Namespace: "dummy-bar",
				},
			},
		},
	}

	handler := withHTTPTriggerSet(ts)
	h := handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, ts, r.Context().Value(httpTriggerSetKey))
	}))

	recorder := httptest.NewRecorder()
	req, err := http.NewRequest("GET", "http://foobar.com", nil)
	assert.Nil(t, err)
	h.ServeHTTP(recorder, req)
	assert.Equal(t, 200, recorder.Code)
}

func Test_computeOpenAPIPath(t *testing.T) {
	type args struct {
		spec fv1.HTTPTriggerSpec
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "test-1",
			args: args{
				spec: fv1.HTTPTriggerSpec{
					Prefix:      stringPtr("/prefix"),
					RelativeURL: "/relative",
				},
			},
			want: "/prefix",
		},
		{
			name: "test-2",
			args: args{
				spec: fv1.HTTPTriggerSpec{
					RelativeURL: "/relative",
				},
			},
			want: "/relative",
		},
		{
			name: "test-3",
			args: args{
				spec: fv1.HTTPTriggerSpec{
					Prefix:      stringPtr("/prefix/"),
					RelativeURL: "/relative",
				},
			},
			want: "/prefix/",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, computeOpenAPIPath(tt.args.spec))
		})
	}
}

func stringPtr(s string) *string {
	return &s
}

func Test_openAPIHandler(t *testing.T) {
	config := zap.NewDevelopmentConfig()
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	logger, err := config.Build()
	assert.Nil(t, err)

	type args struct {
		ts *HTTPTriggerSet
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "test-1",
			args: args{
				ts: &HTTPTriggerSet{
					logger: logger,
					triggers: []fv1.HTTPTrigger{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name:      "dummy",
								Namespace: "dummy-bar",
							},
							Spec: fv1.HTTPTriggerSpec{
								Prefix: stringPtr("/prefix"),
							},
						},
					},
				},
			},
			want: `{"openapi":"3.0.0","info":{"description":"Auto-generated OpenAPI spec for Fission HTTP Triggers","title":"Fission HTTP Triggers","version":"1.0.0"},"paths":{"/prefix":{"post":{"responses":{"200":{"description":"Successful response"}}},"summary":"Trigger: dummy"}}}`,
		},
		{
			name: "test-2",
			args: args{
				ts: &HTTPTriggerSet{
					logger: logger,
					triggers: []fv1.HTTPTrigger{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name:      "dummy",
								Namespace: "dummy-bar",
							},
							Spec: fv1.HTTPTriggerSpec{
								Prefix: stringPtr("/prefix"),
								OpenAPISpec: &fv1.OpenAPISpec{
									PathItem: openapi3.PathItem{
										Summary: "Trigger: dummy",
										Get: &openapi3.Operation{
											OperationID: "get_dummy",
											Parameters: openapi3.Parameters{
												&openapi3.ParameterRef{
													Value: &openapi3.Parameter{
														Name: "suffix",
														In:   "path",
														Schema: &openapi3.SchemaRef{
															Value: &openapi3.Schema{
																Type: openapi3.NewStringSchema().Type,
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			want: `{"openapi":"3.0.0","info":{"description":"Auto-generated OpenAPI spec for Fission HTTP Triggers","title":"Fission HTTP Triggers","version":"1.0.0"},"paths":{"/prefix":{"get":{"operationId":"get_dummy","parameters":[{"in":"path","name":"suffix","schema":{"type":"string"}}],"responses":null},"summary":"Trigger: dummy"}}}`,
		},
		{
			name: "test-3",
			args: args{
				ts: &HTTPTriggerSet{
					logger: logger,
					triggers: []fv1.HTTPTrigger{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name:      "dummy",
								Namespace: "dummy-bar",
							},
							Spec: fv1.HTTPTriggerSpec{
								Prefix: stringPtr("/prefix"),
								OpenAPISpec: &fv1.OpenAPISpec{
									PathItem: openapi3.PathItem{
										Summary: "Trigger: dummy",
										Get: &openapi3.Operation{
											OperationID: "get_dummy",
										},
										Servers: openapi3.Servers{
											{
												URL:         "https://api.example.com",
												Description: "Production server",
											},
										},
									},
								},
							},
						},
					},
				},
			},
			want: `{"openapi":"3.0.0","info":{"description":"Auto-generated OpenAPI spec for Fission HTTP Triggers","title":"Fission HTTP Triggers","version":"1.0.0"},"paths":{"/prefix":{"get":{"operationId":"get_dummy","responses":null},"servers":[{"description":"Production server","url":"https://api.example.com"}],"summary":"Trigger: dummy"}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req, err := http.NewRequest("GET", "http://foobar.com", nil)
			assert.Nil(t, err)
			withHTTPTriggerSet(tt.args.ts)(
				http.HandlerFunc(openAPIHandler),
			).ServeHTTP(
				recorder, req,
			)
			assert.Equal(t, 200, recorder.Code)
			assert.Equal(t, "application/json; charset=utf-8", recorder.Header().Get("Content-Type"))
			assert.NotEmpty(t, recorder.Body.String())
			assert.Contains(t, recorder.Body.String(), "openapi")
			assert.Equal(t, tt.want, recorder.Body.String())
		})
	}
}
