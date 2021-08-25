/*
Copyright 2018 The Fission Authors.

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

package poolmgr

import fv1 "github.com/fission/fission/pkg/apis/core/v1"

func getEnvPoolSize(env *fv1.Environment) int32 {
	var poolsize int32
	if env.Spec.Version < 3 {
		poolsize = 3
	} else {
		poolsize = int32(env.Spec.Poolsize)
	}
	return poolsize
}

func getSpecializedPodLabels(env *fv1.Environment) map[string]string {
	specialPodLabels := make(map[string]string)
	specialPodLabels[fv1.EXECUTOR_TYPE] = string(fv1.ExecutorTypePoolmgr)
	specialPodLabels[fv1.ENVIRONMENT_NAME] = env.ObjectMeta.Name
	specialPodLabels[fv1.ENVIRONMENT_NAMESPACE] = env.ObjectMeta.Namespace
	specialPodLabels[fv1.ENVIRONMENT_UID] = string(env.ObjectMeta.UID)
	specialPodLabels["managed"] = "false"
	return specialPodLabels
}
