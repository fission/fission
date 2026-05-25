// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"reflect"
	"testing"

	v1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestGetIngressSpec(t *testing.T) {
	type args struct {
		ingressNS string
		trigger   *fv1.HTTPTrigger
	}
	var pathType = v1.PathTypeImplementationSpecific
	tests := []struct {
		name string
		args args
		want *v1.Ingress
	}{
		{
			name: "host-backward-compatibility",
			args: args{
				ingressNS: "foobarNS",
				trigger: &fv1.HTTPTrigger{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: "bar",
					},
					Spec: fv1.HTTPTriggerSpec{
						Host:        "test.com",
						RelativeURL: "/foo/bar",
						FunctionReference: fv1.FunctionReference{
							Name: "foofunc",
						},
						IngressConfig: fv1.IngressConfig{
							Annotations: nil,
						},
					},
				},
			},
			want: &v1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"triggerName":      "foo",
						"functionName":     "foofunc",
						"triggerNamespace": "bar",
					},
					Name:        "foo",
					Namespace:   "foobarNS",
					Annotations: nil,
				},
				Spec: v1.IngressSpec{
					Rules: []v1.IngressRule{
						{
							Host: "test.com",
							IngressRuleValue: v1.IngressRuleValue{
								HTTP: &v1.HTTPIngressRuleValue{
									Paths: []v1.HTTPIngressPath{
										{
											Backend: v1.IngressBackend{
												Service: &v1.IngressServiceBackend{
													Name: "router",
													Port: v1.ServiceBackendPort{
														Number: 80,
													},
												},
											},
											Path:     "/foo/bar",
											PathType: &pathType,
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "create-ingress-with-only-annotations",
			args: args{
				ingressNS: "foobarNS",
				trigger: &fv1.HTTPTrigger{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: "bar",
					},
					Spec: fv1.HTTPTriggerSpec{
						RelativeURL: "/foo/bar",
						FunctionReference: fv1.FunctionReference{
							Name: "foofunc",
						},
						IngressConfig: fv1.IngressConfig{
							Annotations: map[string]string{
								"key": "value",
							},
						},
					},
				},
			},
			want: &v1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"triggerName":      "foo",
						"functionName":     "foofunc",
						"triggerNamespace": "bar",
					},
					Name:      "foo",
					Namespace: "foobarNS",
					Annotations: map[string]string{
						"key": "value",
					},
				},
				Spec: v1.IngressSpec{
					Rules: []v1.IngressRule{
						{
							Host: "",
							IngressRuleValue: v1.IngressRuleValue{
								HTTP: &v1.HTTPIngressRuleValue{
									Paths: []v1.HTTPIngressPath{
										{
											Backend: v1.IngressBackend{
												Service: &v1.IngressServiceBackend{
													Name: "router",
													Port: v1.ServiceBackendPort{
														Number: 80,
													},
												},
											},
											Path:     "/foo/bar",
											PathType: &pathType,
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "create-ingress-with-only-rule",
			args: args{
				ingressNS: "foobarNS",
				trigger: &fv1.HTTPTrigger{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: "bar",
					},
					Spec: fv1.HTTPTriggerSpec{
						RelativeURL: "/foo/{bar}",
						FunctionReference: fv1.FunctionReference{
							Name: "foofunc",
						},
						IngressConfig: fv1.IngressConfig{
							Annotations: nil,
							Path:        "/foo/bar",
							Host:        "test.com",
						},
					},
				},
			},
			want: &v1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"triggerName":      "foo",
						"functionName":     "foofunc",
						"triggerNamespace": "bar",
					},
					Name:        "foo",
					Namespace:   "foobarNS",
					Annotations: nil,
				},
				Spec: v1.IngressSpec{
					Rules: []v1.IngressRule{
						{
							Host: "test.com",
							IngressRuleValue: v1.IngressRuleValue{
								HTTP: &v1.HTTPIngressRuleValue{
									Paths: []v1.HTTPIngressPath{
										{
											Backend: v1.IngressBackend{
												Service: &v1.IngressServiceBackend{
													Name: "router",
													Port: v1.ServiceBackendPort{
														Number: 80,
													},
												},
											},
											Path:     "/foo/bar",
											PathType: &pathType,
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "create-ingress-with-empty-rule-host",
			args: args{
				ingressNS: "foobarNS",
				trigger: &fv1.HTTPTrigger{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: "bar",
					},
					Spec: fv1.HTTPTriggerSpec{
						RelativeURL: "/foo/{bar}",
						FunctionReference: fv1.FunctionReference{
							Name: "foofunc",
						},
						IngressConfig: fv1.IngressConfig{
							Annotations: nil,
							Path:        "/foo/bar",
							Host:        "",
						},
					},
				},
			},
			want: &v1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"triggerName":      "foo",
						"functionName":     "foofunc",
						"triggerNamespace": "bar",
					},
					Name:        "foo",
					Namespace:   "foobarNS",
					Annotations: nil,
				},
				Spec: v1.IngressSpec{
					Rules: []v1.IngressRule{
						{
							Host: "",
							IngressRuleValue: v1.IngressRuleValue{
								HTTP: &v1.HTTPIngressRuleValue{
									Paths: []v1.HTTPIngressPath{
										{
											Backend: v1.IngressBackend{
												Service: &v1.IngressServiceBackend{
													Name: "router",
													Port: v1.ServiceBackendPort{
														Number: 80,
													},
												},
											},
											Path:     "/foo/{bar}",
											PathType: &pathType,
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "create-ingress-with-empty-rule-path",
			args: args{
				ingressNS: "foobarNS",
				trigger: &fv1.HTTPTrigger{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: "bar",
					},
					Spec: fv1.HTTPTriggerSpec{
						RelativeURL: "/foo/{bar}",
						FunctionReference: fv1.FunctionReference{
							Name: "foofunc",
						},
						IngressConfig: fv1.IngressConfig{
							Annotations: nil,
							Path:        "",
							Host:        "test.com",
						},
					},
				},
			},
			want: &v1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"triggerName":      "foo",
						"functionName":     "foofunc",
						"triggerNamespace": "bar",
					},
					Name:        "foo",
					Namespace:   "foobarNS",
					Annotations: nil,
				},
				Spec: v1.IngressSpec{
					Rules: []v1.IngressRule{
						{
							Host: "",
							IngressRuleValue: v1.IngressRuleValue{
								HTTP: &v1.HTTPIngressRuleValue{
									Paths: []v1.HTTPIngressPath{
										{
											Backend: v1.IngressBackend{
												Service: &v1.IngressServiceBackend{
													Name: "router",
													Port: v1.ServiceBackendPort{
														Number: 80,
													},
												},
											},
											Path:     "/foo/{bar}",
											PathType: &pathType,
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "create-ingress-with-host-and-rule",
			args: args{
				ingressNS: "foobarNS",
				trigger: &fv1.HTTPTrigger{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: "bar",
					},
					Spec: fv1.HTTPTriggerSpec{
						Host:        "example.com",
						RelativeURL: "/foo/{bar}",
						FunctionReference: fv1.FunctionReference{
							Name: "foofunc",
						},
						IngressConfig: fv1.IngressConfig{
							Annotations: nil,
							Path:        "/foo/bar",
							Host:        "test.com",
						},
					},
				},
			},
			want: &v1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"triggerName":      "foo",
						"functionName":     "foofunc",
						"triggerNamespace": "bar",
					},
					Name:        "foo",
					Namespace:   "foobarNS",
					Annotations: nil,
				},
				Spec: v1.IngressSpec{
					Rules: []v1.IngressRule{
						{
							Host: "test.com",
							IngressRuleValue: v1.IngressRuleValue{
								HTTP: &v1.HTTPIngressRuleValue{
									Paths: []v1.HTTPIngressPath{
										{
											Backend: v1.IngressBackend{
												Service: &v1.IngressServiceBackend{
													Name: "router",
													Port: v1.ServiceBackendPort{
														Number: 80,
													},
												},
											},
											Path:     "/foo/bar",
											PathType: &pathType,
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "create-ingress-with-wildecard-rule-host",
			args: args{
				ingressNS: "foobarNS",
				trigger: &fv1.HTTPTrigger{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: "bar",
					},
					Spec: fv1.HTTPTriggerSpec{
						RelativeURL: "/foo/{bar}",
						FunctionReference: fv1.FunctionReference{
							Name: "foofunc",
						},
						IngressConfig: fv1.IngressConfig{
							Annotations: nil,
							Path:        "/foo/bar",
							Host:        "*",
						},
					},
				},
			},
			want: &v1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"triggerName":      "foo",
						"functionName":     "foofunc",
						"triggerNamespace": "bar",
					},
					Name:        "foo",
					Namespace:   "foobarNS",
					Annotations: nil,
				},
				Spec: v1.IngressSpec{
					Rules: []v1.IngressRule{
						{
							Host: "",
							IngressRuleValue: v1.IngressRuleValue{
								HTTP: &v1.HTTPIngressRuleValue{
									Paths: []v1.HTTPIngressPath{
										{
											Backend: v1.IngressBackend{
												Service: &v1.IngressServiceBackend{
													Name: "router",
													Port: v1.ServiceBackendPort{
														Number: 80,
													},
												},
											},
											Path:     "/foo/bar",
											PathType: &pathType,
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "tls-setup",
			args: args{
				ingressNS: "foobarNS",
				trigger: &fv1.HTTPTrigger{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: "bar",
					},
					Spec: fv1.HTTPTriggerSpec{
						RelativeURL: "/foo/bar",
						FunctionReference: fv1.FunctionReference{
							Name: "foofunc",
						},
						IngressConfig: fv1.IngressConfig{
							Annotations: map[string]string{
								"key": "value",
							},
							Host: "test.com",
							TLS:  "foobar",
						},
					},
				},
			},
			want: &v1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"triggerName":      "foo",
						"functionName":     "foofunc",
						"triggerNamespace": "bar",
					},
					Name:      "foo",
					Namespace: "foobarNS",
					Annotations: map[string]string{
						"key": "value",
					},
				},
				Spec: v1.IngressSpec{
					TLS: []v1.IngressTLS{
						{
							Hosts: []string{
								"test.com",
							},
							SecretName: "foobar",
						},
					},
					Rules: []v1.IngressRule{
						{
							Host: "",
							IngressRuleValue: v1.IngressRuleValue{
								HTTP: &v1.HTTPIngressRuleValue{
									Paths: []v1.HTTPIngressPath{
										{
											Backend: v1.IngressBackend{
												Service: &v1.IngressServiceBackend{
													Name: "router",
													Port: v1.ServiceBackendPort{
														Number: 80,
													},
												},
											},
											Path:     "/foo/bar",
											PathType: &pathType,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetIngressSpec(tt.args.ingressNS, tt.args.trigger); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetIngressSpec() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetDeployLabels(t *testing.T) {
	type args struct {
		trigger *fv1.HTTPTrigger
	}
	// TODO: support function weight
	tests := []struct {
		name string
		args args
		want map[string]string
	}{
		{
			name: "getdeploylabels",
			args: args{
				trigger: &fv1.HTTPTrigger{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: "bar",
					},
					Spec: fv1.HTTPTriggerSpec{
						FunctionReference: fv1.FunctionReference{
							Type:            "name",
							Name:            "foobar",
							FunctionWeights: nil,
						},
					},
				},
			},
			want: map[string]string{
				"triggerName":      "foo",
				"functionName":     "foobar",
				"triggerNamespace": "bar",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetDeployLabels(tt.args.trigger); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetDeployLabels() = %v, want %v", got, tt.want)
			}
		})
	}
}
