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

package utils

import (
	v1 "k8s.io/api/core/v1"
)

// IsReadyPod checks both all containers in a pod are ready and whether
// the .metadata.DeletionTimestamp is nil.
func IsReadyPod(pod *v1.Pod) bool {
	// since its a utility function, just ensuring there is no nil pointer exception
	if pod == nil {
		return false
	}

	// pod is in "Terminating" status if deletionTimestamp is not nil
	// https://github.com/kubernetes/kubernetes/issues/61376
	if pod.DeletionTimestamp != nil {
		return false
	}

	// pod does not have an IP address allocated to it yet
	if pod.Status.PodIP == "" {
		return false
	}

	for _, cStatus := range pod.Status.ContainerStatuses {
		if !cStatus.Ready {
			return false
		}
	}

	return true
}

func IsPodTerminated(pod *v1.Pod) bool {
	if phase := pod.Status.Phase; phase != v1.PodPending && phase != v1.PodRunning && phase != v1.PodUnknown {
		return true
	}
	return false
}

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
