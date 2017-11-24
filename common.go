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
	apiv1 "k8s.io/client-go/pkg/api/v1"
)

func UrlForFunction(name string) string {
	prefix := "/fission-function"
	return fmt.Sprintf("%v/%v", prefix, name)
}

func K8sEnvVars(env []EnvVar) []apiv1.EnvVar {
	envVars := make([]apiv1.EnvVar, len(env))
	for k, v := range env {
		envVars[k] = apiv1.EnvVar{
			Name:  v.Name,
			Value: v.Value,
		}
	}
	return envVars
}