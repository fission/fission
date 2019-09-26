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

package httptrigger

import (
	"reflect"
	"testing"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
)

func Test_GetIngressConfig(t *testing.T) {
	type args struct {
		ingressConfig       *fv1.IngressConfig
		annotations         []string
		rule                string
		fallbackRelativeURL string
		tls                 string
	}
	tests := []struct {
		name    string
		args    args
		want    *fv1.IngressConfig
		wantErr bool
	}{
		{
			name: "pass-nil-ingressconfig-pointer",
			args: args{
				ingressConfig:       nil,
				annotations:         []string{"foo=bar", "bar=foo"},
				rule:                "test.com=/foo/bar",
				fallbackRelativeURL: "/test",
			},
			want: &fv1.IngressConfig{
				Annotations: map[string]string{
					"foo": "bar",
					"bar": "foo",
				},
				Host: "test.com",
				Path: "/foo/bar",
			},
			wantErr: false,
		},
		{
			name: "pass-non-nil-ingressconfig-pointer",
			args: args{
				ingressConfig: &fv1.IngressConfig{
					Annotations: map[string]string{
						"hello": "world",
					},
					Host: "foo",
					Path: "bar",
				},
				annotations:         []string{"foo=bar", "bar=foo"},
				rule:                "test.com=/foo/bar",
				fallbackRelativeURL: "/test",
			},
			want: &fv1.IngressConfig{
				Annotations: map[string]string{
					"foo":   "bar",
					"bar":   "foo",
					"hello": "world",
				},
				Host: "test.com",
				Path: "/foo/bar",
			},
			wantErr: false,
		},
		{
			name: "ingressconfig-with-nil-annotations",
			args: args{
				ingressConfig: &fv1.IngressConfig{
					Annotations: nil,
					Host:        "foo",
					Path:        "bar",
				},
				annotations:         []string{"foo=bar", "bar=foo"},
				rule:                "test.com=/foo/bar",
				fallbackRelativeURL: "/test",
			},
			want: &fv1.IngressConfig{
				Annotations: map[string]string{
					"foo": "bar",
					"bar": "foo",
				},
				Host: "test.com",
				Path: "/foo/bar",
			},
			wantErr: false,
		},
		{
			name: "remove-annotations-from-ingressconfig",
			args: args{
				ingressConfig: &fv1.IngressConfig{
					Annotations: map[string]string{
						"hello": "world",
					},
				},
				annotations:         []string{"-"},
				rule:                "test.com=/foo/bar",
				fallbackRelativeURL: "/test",
			},
			want: &fv1.IngressConfig{
				Annotations: nil,
				Host:        "test.com",
				Path:        "/foo/bar",
			},
			wantErr: false,
		},
		{
			name: "remove-rule-from-ingressconfig",
			args: args{
				ingressConfig: &fv1.IngressConfig{
					Annotations: map[string]string{
						"hello": "world",
					},
				},
				annotations:         []string{"-"},
				rule:                "-",
				fallbackRelativeURL: "/test",
			},
			want: &fv1.IngressConfig{
				Annotations: nil,
				Host:        "*",
				Path:        "/test",
			},
			wantErr: false,
		},
		{
			name: "wrong-annotations-value-1",
			args: args{
				ingressConfig:       nil,
				annotations:         []string{"a"},
				rule:                "-",
				fallbackRelativeURL: "/test",
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "wrong-annotations-value-2",
			args: args{
				ingressConfig:       nil,
				annotations:         []string{"a=b=c"},
				rule:                "-",
				fallbackRelativeURL: "/test",
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "wrong-rule-value-1",
			args: args{
				ingressConfig: &fv1.IngressConfig{
					Annotations: map[string]string{
						"hello": "world",
					},
				},
				annotations:         []string{"a=b"},
				rule:                "a",
				fallbackRelativeURL: "/test",
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "wrong-rule-value-2",
			args: args{
				ingressConfig: &fv1.IngressConfig{
					Annotations: map[string]string{
						"hello": "world",
					},
				},
				annotations:         []string{"a=b"},
				rule:                "a=b=c",
				fallbackRelativeURL: "/test",
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "ingressconfog-with-only-fallback-rul",
			args: args{
				ingressConfig:       nil,
				annotations:         nil,
				rule:                "",
				fallbackRelativeURL: "/test",
			},
			want: &fv1.IngressConfig{
				Annotations: nil,
				Host:        "*",
				Path:        "/test",
			},
			wantErr: false,
		},
		{
			name: "backward-compatibility-test",
			args: args{
				ingressConfig: &fv1.IngressConfig{
					Annotations: nil,
					Host:        "",
					Path:        "",
				},
				annotations:         nil,
				rule:                "",
				fallbackRelativeURL: "/test",
			},
			want: &fv1.IngressConfig{
				Annotations: nil,
				Host:        "*",
				Path:        "/test",
			},
			wantErr: false,
		},
		{
			name: "preserve-annotation-if-nothing-change",
			args: args{
				ingressConfig: &fv1.IngressConfig{
					Annotations: map[string]string{
						"a": "b",
					},
					Host: "test.com",
					Path: "/foo/bar",
				},
				annotations:         nil,
				rule:                "",
				fallbackRelativeURL: "/test",
			},
			want: &fv1.IngressConfig{
				Annotations: map[string]string{
					"a": "b",
				},
				Host: "test.com",
				Path: "/foo/bar",
			},
			wantErr: false,
		},
		{
			name: "tls-setup",
			args: args{
				ingressConfig: &fv1.IngressConfig{
					Annotations: map[string]string{
						"a": "b",
					},
					Host: "test.com",
					Path: "/foo/bar",
					TLS:  "",
				},
				annotations:         nil,
				rule:                "",
				fallbackRelativeURL: "/test",
				tls:                 "dummy",
			},
			want: &fv1.IngressConfig{
				Annotations: map[string]string{
					"a": "b",
				},
				Host: "test.com",
				Path: "/foo/bar",
				TLS:  "dummy",
			},
			wantErr: false,
		},
		{
			name: "same-tls",
			args: args{
				ingressConfig:       nil,
				annotations:         nil,
				rule:                "",
				fallbackRelativeURL: "/test",
				tls:                 "dummy",
			},
			want: &fv1.IngressConfig{
				Annotations: nil,
				Host:        "*",
				Path:        "/test",
				TLS:         "dummy",
			},
			wantErr: false,
		},
		{
			name: "replace-tls",
			args: args{
				ingressConfig: &fv1.IngressConfig{
					Annotations: map[string]string{
						"a": "b",
					},
					Host: "test.com",
					Path: "/foo/bar",
					TLS:  "foobar",
				},
				annotations:         nil,
				rule:                "",
				fallbackRelativeURL: "/test",
				tls:                 "dummy",
			},
			want: &fv1.IngressConfig{
				Annotations: map[string]string{
					"a": "b",
				},
				Host: "test.com",
				Path: "/foo/bar",
				TLS:  "dummy",
			},
			wantErr: false,
		},
		{
			name: "remove-tls",
			args: args{
				ingressConfig: &fv1.IngressConfig{
					Annotations: map[string]string{
						"a": "b",
					},
					Host: "test.com",
					Path: "/foo/bar",
					TLS:  "foobar",
				},
				annotations:         nil,
				rule:                "",
				fallbackRelativeURL: "/test",
				tls:                 "-",
			},
			want: &fv1.IngressConfig{
				Annotations: map[string]string{
					"a": "b",
				},
				Host: "test.com",
				Path: "/foo/bar",
				TLS:  "",
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetIngressConfig(tt.args.annotations, tt.args.rule, tt.args.tls, tt.args.fallbackRelativeURL, tt.args.ingressConfig)
			if (err != nil) != tt.wantErr {
				t.Errorf("getIngressConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("%v %v %v %v", got.Annotations == nil, tt.want.Annotations == nil, got.Path == tt.want.Path, got.Host == tt.want.Host)
				t.Errorf("getIngressConfig() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getIngressAnnotations(t *testing.T) {
	type args struct {
		annotations []string
	}
	tests := []struct {
		name       string
		args       args
		wantRemove bool
		wantAnns   map[string]string
		wantErr    bool
	}{
		{
			name: "get-annotations",
			args: args{
				annotations: []string{"a=b", "c=d"},
			},
			wantRemove: false,
			wantAnns: map[string]string{
				"a": "b",
				"c": "d",
			},
			wantErr: false,
		},
		{
			name: "remove-all-annotations",
			args: args{
				annotations: []string{"-", "c=d"},
			},
			wantRemove: true,
			wantAnns:   nil,
			wantErr:    false,
		},
		{
			name: "incorrect-annotation",
			args: args{
				annotations: []string{"a==b"},
			},
			wantRemove: false,
			wantAnns:   nil,
			wantErr:    true,
		},
		{
			name: "zero-annotations-1",
			args: args{
				annotations: []string{},
			},
			wantRemove: false,
			wantAnns:   nil,
			wantErr:    false,
		},
		{
			name: "zero-annotations-2",
			args: args{
				annotations: nil,
			},
			wantRemove: false,
			wantAnns:   nil,
			wantErr:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRemove, gotAnns, err := getIngressAnnotations(tt.args.annotations)
			if (err != nil) != tt.wantErr {
				t.Errorf("getIngressAnnotations() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotRemove != tt.wantRemove {
				t.Errorf("getIngressAnnotations() gotRemove = %v, want %v", gotRemove, tt.wantRemove)
			}
			if !reflect.DeepEqual(gotAnns, tt.wantAnns) {
				t.Errorf("getIngressAnnotations() gotAnns = %v, want %v", gotAnns, tt.wantAnns)
			}
		})
	}
}

func Test_getIngressHostRule(t *testing.T) {
	type args struct {
		rule         string
		fallbackPath string
	}
	tests := []struct {
		name      string
		args      args
		wantEmpty bool
		wantHost  string
		wantPath  string
		wantErr   bool
	}{
		{
			name: "get-rule",
			args: args{
				rule:         "a=b",
				fallbackPath: "/foo",
			},
			wantEmpty: false,
			wantHost:  "a",
			wantPath:  "b",
			wantErr:   false,
		},
		{
			name: "remove-rule",
			args: args{
				rule:         "-",
				fallbackPath: "/foo",
			},
			wantEmpty: false,
			wantHost:  "*",
			wantPath:  "/foo",
			wantErr:   false,
		},
		{
			name: "empty-rule",
			args: args{
				rule:         "",
				fallbackPath: "/foo",
			},
			wantEmpty: true,
			wantHost:  "",
			wantPath:  "",
			wantErr:   false,
		},
		{
			name: "empty-host",
			args: args{
				rule:         "=/aasd",
				fallbackPath: "/foo",
			},
			wantEmpty: false,
			wantHost:  "",
			wantPath:  "",
			wantErr:   true,
		},
		{
			name: "empty-path",
			args: args{
				rule:         "test.com=",
				fallbackPath: "/foo",
			},
			wantEmpty: false,
			wantHost:  "",
			wantPath:  "",
			wantErr:   true,
		},
		{
			name: "empty-fallback-url",
			args: args{
				rule:         "test.com=",
				fallbackPath: "",
			},
			wantEmpty: false,
			wantHost:  "",
			wantPath:  "",
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEmpty, gotHost, gotPath, err := getIngressHostRule(tt.args.rule, tt.args.fallbackPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("getIngressHostRule() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotEmpty != tt.wantEmpty {
				t.Errorf("getIngressHostRule() gotEmpty = %v, want %v", gotEmpty, tt.wantEmpty)
			}
			if gotHost != tt.wantHost {
				t.Errorf("getIngressHostRule() gotHost = %v, want %v", gotHost, tt.wantHost)
			}
			if gotPath != tt.wantPath {
				t.Errorf("getIngressHostRule() gotPath = %v, want %v", gotPath, tt.wantPath)
			}
		})
	}
}

func Test_getIngressTLS(t *testing.T) {
	type args struct {
		secret string
	}
	tests := []struct {
		name       string
		args       args
		wantRemove bool
		wantTls    string
	}{
		{
			name: "tls-setup",
			args: args{
				secret: "foobar",
			},
			wantRemove: false,
			wantTls:    "foobar",
		},
		{
			name: "remove-tls",
			args: args{
				secret: "-",
			},
			wantRemove: true,
			wantTls:    "",
		},
		{
			name: "empty-tls",
			args: args{
				secret: "",
			},
			wantRemove: false,
			wantTls:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRemove, gotTls := getIngressTLS(tt.args.secret)
			if gotRemove != tt.wantRemove {
				t.Errorf("getIngressTLS() gotRemove = %v, want %v", gotRemove, tt.wantRemove)
			}
			if gotTls != tt.wantTls {
				t.Errorf("getIngressTLS() gotTls = %v, want %v", gotTls, tt.wantTls)
			}
		})
	}
}
