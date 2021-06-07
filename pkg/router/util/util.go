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

package util

import (
	"k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func GetIngressSpec(namespace string, trigger *fv1.HTTPTrigger) *v1beta1.Ingress {
	// TODO: remove backward compatibility
	// todo: consider prefix support
	host, path := trigger.Spec.Host, trigger.Spec.RelativeURL
	if len(trigger.Spec.IngressConfig.Host) > 0 && len(trigger.Spec.IngressConfig.Path) > 0 {
		host, path = trigger.Spec.IngressConfig.Host, trigger.Spec.IngressConfig.Path
	}

	// In Ingress, to accept requests from all host, the host field will
	// be an empty string instead of "*" shown in kubectl. So replace it
	// with empty string
	if host == "*" {
		host = "" // wildcard Ingress host
	}

	var ingTLS []v1beta1.IngressTLS
	if len(trigger.Spec.IngressConfig.TLS) > 0 {
		ingTLS = []v1beta1.IngressTLS{
			{
				Hosts: []string{
					trigger.Spec.IngressConfig.Host,
				},
				SecretName: trigger.Spec.IngressConfig.TLS,
			},
		}
	}

	ing := &v1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Labels: GetDeployLabels(trigger),
			Name:   trigger.ObjectMeta.Name,
			// The Ingress NS MUST be same as Router NS, check long discussion:
			// https://github.com/kubernetes/kubernetes/issues/17088
			// We need to revisit this in future, once Kubernetes supports cross namespace ingress
			Namespace:   namespace,
			Annotations: trigger.Spec.IngressConfig.Annotations,
		},
		Spec: v1beta1.IngressSpec{
			TLS: ingTLS,
			Rules: []v1beta1.IngressRule{
				{
					Host: host,
					IngressRuleValue: v1beta1.IngressRuleValue{
						HTTP: &v1beta1.HTTPIngressRuleValue{
							Paths: []v1beta1.HTTPIngressPath{
								{
									Backend: v1beta1.IngressBackend{
										ServiceName: "router",
										ServicePort: intstr.IntOrString{
											Type:   intstr.Int,
											IntVal: 80,
										},
									},
									Path: path,
								},
							},
						},
					},
				},
			},
		},
	}
	return ing
}

func GetDeployLabels(trigger *fv1.HTTPTrigger) map[string]string {
	// TODO: support function weight
	return map[string]string{
		"triggerName":      trigger.ObjectMeta.Name,
		"functionName":     trigger.Spec.FunctionReference.Name,
		"triggerNamespace": trigger.ObjectMeta.Namespace,
	}
}
