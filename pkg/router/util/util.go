// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"net/http"

	v1 "k8s.io/api/networking/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func GetIngressSpec(namespace string, trigger *fv1.HTTPTrigger) *v1.Ingress {
	// TODO: remove backward compatibility
	host, path := trigger.Spec.Host, trigger.Spec.RelativeURL
	if trigger.Spec.Prefix != nil && *trigger.Spec.Prefix != "" {
		path = *trigger.Spec.Prefix
	}
	if len(trigger.Spec.IngressConfig.Host) > 0 && len(trigger.Spec.IngressConfig.Path) > 0 {
		host, path = trigger.Spec.IngressConfig.Host, trigger.Spec.IngressConfig.Path
	}
	annotations := trigger.Spec.IngressConfig.Annotations
	tlsHost, tlsSecret := trigger.Spec.IngressConfig.Host, trigger.Spec.IngressConfig.TLS

	// RouteConfig (the provider-neutral successor) takes precedence over the
	// deprecated Host/IngressConfig fields when it selects the ingress provider.
	if rc := trigger.Spec.RouteConfig; rc != nil && rc.Provider == fv1.RouteProviderIngress {
		if len(rc.Hostnames) > 0 {
			host, tlsHost = rc.Hostnames[0], rc.Hostnames[0]
		}
		if rc.Path != "" {
			path = rc.Path
		}
		annotations = rc.Annotations
		tlsSecret = rc.TLS
	}

	// In Ingress, to accept requests from all host, the host field will
	// be an empty string instead of "*" shown in kubectl. So replace it
	// with empty string
	if host == "*" {
		host = "" // wildcard Ingress host
	}

	var ingTLS []v1.IngressTLS
	if len(tlsSecret) > 0 {
		ingTLS = []v1.IngressTLS{
			{
				Hosts: []string{
					tlsHost,
				},
				SecretName: tlsSecret,
			},
		}
	}

	var pathType = v1.PathTypeImplementationSpecific
	ing := &v1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Labels: GetDeployLabels(trigger),
			Name:   trigger.Name,
			// The Ingress NS MUST be same as Router NS, check long discussion:
			// https://github.com/kubernetes/kubernetes/issues/17088
			// We need to revisit this in future, once Kubernetes supports cross namespace ingress
			Namespace:   namespace,
			Annotations: annotations,
		},
		Spec: v1.IngressSpec{
			TLS: ingTLS,
			Rules: []v1.IngressRule{
				{
					Host: host,
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
									Path:     path,
									PathType: &pathType,
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

// GetHTTPRouteSpec builds the desired Gateway API HTTPRoute for a trigger that
// requests the gateway provider. The route is created in namespace (the
// router's namespace), named after the trigger, and routes matched requests to
// the router Service on port 80 — the same backend the Ingress path uses.
// parentRefs come from the trigger's RouteConfig.Gateway.ParentRefs; when the
// trigger lists none, defaultParentRefs (from the router's configuration) is
// used so an operator can set a cluster-wide default Gateway. TLS is the
// Gateway listener's responsibility in attach mode and is not set here.
func GetHTTPRouteSpec(namespace string, trigger *fv1.HTTPTrigger, defaultParentRefs []gwapiv1.ParentReference) *gwapiv1.HTTPRoute {
	rc := trigger.Spec.RouteConfig

	parentRefs := defaultParentRefs
	if rc != nil && rc.Gateway != nil && len(rc.Gateway.ParentRefs) > 0 {
		parentRefs = make([]gwapiv1.ParentReference, 0, len(rc.Gateway.ParentRefs))
		for _, ref := range rc.Gateway.ParentRefs {
			parentRefs = append(parentRefs, toParentReference(ref, namespace))
		}
	}

	var hostnames []gwapiv1.Hostname
	if rc != nil {
		for _, h := range rc.Hostnames {
			if h == "" || h == "*" {
				// An empty/"*" hostname means "all hosts" — represented by an
				// empty hostnames list on the HTTPRoute.
				continue
			}
			hostnames = append(hostnames, gwapiv1.Hostname(h))
		}
	}

	path := "/"
	if rc != nil && rc.Path != "" {
		path = rc.Path
	}

	var annotations map[string]string
	if rc != nil {
		annotations = rc.Annotations
	}

	port := gwapiv1.PortNumber(80)
	return &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      GetDeployLabels(trigger),
			Name:        trigger.Name,
			Namespace:   namespace,
			Annotations: annotations,
		},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{ParentRefs: parentRefs},
			Hostnames:       hostnames,
			Rules: []gwapiv1.HTTPRouteRule{
				{
					Matches: []gwapiv1.HTTPRouteMatch{
						{
							Path: &gwapiv1.HTTPPathMatch{
								Type:  new(gwapiv1.PathMatchPathPrefix),
								Value: &path,
							},
						},
					},
					BackendRefs: []gwapiv1.HTTPBackendRef{
						{
							BackendRef: gwapiv1.BackendRef{
								BackendObjectReference: gwapiv1.BackendObjectReference{
									Name: gwapiv1.ObjectName("router"),
									Port: &port,
								},
							},
						},
					},
				},
			},
		},
	}
}

// toParentReference converts Fission's GatewayParentRef to the Gateway API
// ParentReference. An empty ref.Namespace leaves the reference namespace unset,
// which the Gateway API resolves to the HTTPRoute's own namespace.
func toParentReference(ref fv1.GatewayParentRef, _ string) gwapiv1.ParentReference {
	pr := gwapiv1.ParentReference{Name: gwapiv1.ObjectName(ref.Name)}
	if ref.Namespace != "" {
		pr.Namespace = new(gwapiv1.Namespace(ref.Namespace))
	}
	if ref.SectionName != "" {
		pr.SectionName = new(gwapiv1.SectionName(ref.SectionName))
	}
	if ref.Port != 0 {
		pr.Port = new(ref.Port)
	}
	return pr
}

func GetDeployLabels(trigger *fv1.HTTPTrigger) map[string]string {
	// TODO: support function weight
	return map[string]string{
		"triggerName":      trigger.Name,
		"functionName":     trigger.Spec.FunctionReference.Name,
		"triggerNamespace": trigger.Namespace,
	}
}

func IsWebsocketRequest(request *http.Request) bool {
	return request.Header.Get("Upgrade") == "websocket" &&
		request.Header.Get("Connection") == "Upgrade"
}
