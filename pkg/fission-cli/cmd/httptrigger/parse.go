// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httptrigger

import (
	"fmt"
	"maps"
	"strings"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// GetIngressConfig returns an IngressConfig based on user inputs; return error if any.
func GetIngressConfig(annotations []string, rule string, tls string,
	fallbackRelativeURL string, oldIngressConfig *fv1.IngressConfig) (*fv1.IngressConfig, error) {

	removeAnns, anns, err := getIngressAnnotations(annotations)
	if err != nil {
		return nil, err
	}
	isEmptyRule, host, path, err := getIngressHostRule(rule, fallbackRelativeURL)
	if err != nil {
		return nil, err
	}
	removeTLS, secret := getIngressTLS(tls)

	if oldIngressConfig == nil {
		if isEmptyRule { // assign default value
			host = "*"
			path = fallbackRelativeURL
		}
		return &fv1.IngressConfig{
			Annotations: anns,
			Host:        host,
			Path:        path,
			TLS:         secret,
		}, nil
	}

	if removeAnns {
		oldIngressConfig.Annotations = nil
	} else if len(anns) > 0 {
		if oldIngressConfig.Annotations == nil {
			oldIngressConfig.Annotations = make(map[string]string, len(anns))
		}
		maps.Copy(oldIngressConfig.Annotations, anns)
	}

	if isEmptyRule {
		// an empty rule means no new rule was given,
		// leave host and path intact except when host
		// or path is empty.
		if len(oldIngressConfig.Host) == 0 {
			oldIngressConfig.Host = "*"
		}
		if len(oldIngressConfig.Path) == 0 {
			oldIngressConfig.Path = fallbackRelativeURL
		}
	} else {
		oldIngressConfig.Host = host
		oldIngressConfig.Path = path
	}

	if removeTLS {
		oldIngressConfig.TLS = ""
	} else if len(secret) > 0 {
		oldIngressConfig.TLS = secret
	}

	return oldIngressConfig, nil
}

func getIngressAnnotations(annotations []string) (remove bool, anns map[string]string, err error) {
	if len(annotations) == 0 {
		return false, nil, nil
	}

	anns = make(map[string]string)
	for _, ann := range annotations {
		if ann == "-" {
			// remove all annotations
			return true, nil, nil
		}
		v := strings.SplitN(ann, "=", 2)
		if len(v) != 2 {
			return false, nil, fmt.Errorf("illegal ingress annotation: %v", ann)
		}
		key, val := v[0], v[1]
		anns[key] = val
	}
	return false, anns, nil
}

func getIngressHostRule(rule string, fallbackPath string) (empty bool, host string, path string, err error) {
	if len(fallbackPath) == 0 {
		return false, "", "", fmt.Errorf("fallback url cannot be empty")
	}
	if len(rule) == 0 {
		return true, "", "", nil
	}
	if rule == "-" {
		return false, "*", fallbackPath, nil
	}
	v := strings.SplitN(rule, "=", 2)
	if len(v) != 2 {
		return false, "", "", fmt.Errorf("illegal ingress rule: %v", rule)
	}
	if len(v[0]) == 0 || len(v[1]) == 0 {
		return false, "", "", fmt.Errorf("host (%v) or path (%v) cannot be empty", v[0], v[1])
	}
	return false, v[0], v[1], nil
}

// GetRouteConfig builds a RouteConfig from the --route-* CLI flags, or returns
// nil when no route provider was requested. fallbackPath is the trigger's
// URL/prefix, used as the route path when --route-path is omitted. It is the
// provider-neutral successor to GetIngressConfig.
func GetRouteConfig(provider string, hosts []string, path string, annotations []string,
	tls string, gateways []string, fallbackPath string) (*fv1.RouteConfig, error) {
	if provider == "" {
		return nil, nil
	}
	if provider != fv1.RouteProviderIngress && provider != fv1.RouteProviderGateway {
		return nil, fmt.Errorf("invalid --%s %q: must be one of %q, %q", "route-provider", provider, fv1.RouteProviderIngress, fv1.RouteProviderGateway)
	}

	_, anns, err := getIngressAnnotations(annotations)
	if err != nil {
		return nil, fmt.Errorf("illegal route annotation: %w", err)
	}

	if path == "" {
		path = fallbackPath
	}

	rc := &fv1.RouteConfig{
		Provider:    provider,
		Hostnames:   hosts,
		Path:        path,
		Annotations: anns,
		TLS:         tls,
	}

	if len(gateways) > 0 {
		parentRefs, err := parseGatewayParentRefs(gateways)
		if err != nil {
			return nil, err
		}
		rc.Gateway = &fv1.GatewayRouteConfig{ParentRefs: parentRefs}
	}

	return rc, nil
}

// parseGatewayParentRefs parses --gateway values ("name" or "namespace/name")
// into GatewayParentRefs.
func parseGatewayParentRefs(gateways []string) ([]fv1.GatewayParentRef, error) {
	refs := make([]fv1.GatewayParentRef, 0, len(gateways))
	for _, g := range gateways {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		ref := fv1.GatewayParentRef{}
		if ns, name, ok := strings.Cut(g, "/"); ok {
			if ns == "" || name == "" {
				return nil, fmt.Errorf("invalid --gateway %q: expected \"name\" or \"namespace/name\"", g)
			}
			ref.Namespace = ns
			ref.Name = name
		} else {
			ref.Name = g
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func getIngressTLS(secret string) (remove bool, tls string) {
	switch secret {
	case "-":
		return true, ""
	case "":
		return false, ""
	default:
		return false, secret
	}
}
