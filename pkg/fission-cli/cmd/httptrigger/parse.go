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
		v := strings.Split(ann, "=")
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
	v := strings.Split(rule, "=")
	if len(v) != 2 {
		return false, "", "", fmt.Errorf("illegal ingress rule: %v", rule)
	}
	if len(v[0]) == 0 || len(v[1]) == 0 {
		return false, "", "", fmt.Errorf("host (%v) or path (%v) cannot be empty", v[0], v[1])
	}
	return false, v[0], v[1], nil
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
