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

package lib

import (
	"strconv"

	"github.com/fission/fission/fission/log"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func GetResourceReq(mincpu int, maxcpu int, minmemory int, maxmemory int, resources v1.ResourceRequirements) v1.ResourceRequirements {

	var requestResources map[v1.ResourceName]resource.Quantity

	if len(resources.Requests) == 0 {
		requestResources = make(map[v1.ResourceName]resource.Quantity)
	} else {
		requestResources = resources.Requests
	}

	if mincpu != 0 {
		cpuRequest, err := resource.ParseQuantity(strconv.Itoa(mincpu) + "m")
		if err != nil {
			log.Fatal("Failed to parse mincpu")
		}
		requestResources[v1.ResourceCPU] = cpuRequest
	}

	if minmemory != 0 {
		memRequest, err := resource.ParseQuantity(strconv.Itoa(minmemory) + "Mi")
		if err != nil {
			log.Fatal("Failed to parse minmemory")
		}
		requestResources[v1.ResourceMemory] = memRequest
	}

	var limitResources map[v1.ResourceName]resource.Quantity
	if len(resources.Limits) == 0 {
		limitResources = make(map[v1.ResourceName]resource.Quantity)
	} else {
		limitResources = resources.Limits
	}

	if maxcpu != 0 {
		cpuLimit, err := resource.ParseQuantity(strconv.Itoa(maxcpu) + "m")
		if err != nil {
			log.Fatal("Failed to parse maxcpu")
		}
		limitResources[v1.ResourceCPU] = cpuLimit
	}

	if maxmemory != 0 {
		memLimit, err := resource.ParseQuantity(strconv.Itoa(maxmemory) + "Mi")
		if err != nil {
			log.Fatal("Failed to parse maxmemory")
		}
		limitResources[v1.ResourceMemory] = memLimit
	}

	resources = v1.ResourceRequirements{
		Requests: requestResources,
		Limits:   limitResources,
	}
	return resources
}
