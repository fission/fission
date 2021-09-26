/*
Copyright 2021 The Fission Authors.

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
	v1 "k8s.io/api/core/v1"
)

// PodContainerReadyStatus returns the number of ready containers and total containers present in pod
func PodContainerReadyStatus(pod *v1.Pod) (readyContainers, noOfContainers int) {

	noOfContainers = len(pod.Status.ContainerStatuses)
	readyContainers = 0

	for _, status := range pod.Status.ContainerStatuses {
		if status.Ready {
			readyContainers++
		}
	}

	return
}
