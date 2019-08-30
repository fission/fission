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

package cmd

import (
	"fmt"
	"strconv"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/util"
)

func GetServer(flags cli.Input) *client.Client {
	return util.GetApiClient(flags.GlobalString(FISSION_SERVER))
}

func GetResourceReqs(flags cli.Input, resReqs *v1.ResourceRequirements) (*v1.ResourceRequirements, error) {
	r := &v1.ResourceRequirements{}

	if resReqs != nil {
		r.Requests = resReqs.Requests
		r.Limits = resReqs.Limits
	}

	if len(r.Requests) == 0 {
		r.Requests = make(map[v1.ResourceName]resource.Quantity)
	}

	if len(r.Limits) == 0 {
		r.Limits = make(map[v1.ResourceName]resource.Quantity)
	}

	e := &multierror.Error{}

	if flags.IsSet(RUNTIME_MINCPU) {
		mincpu := flags.Int(RUNTIME_MINCPU)
		cpuRequest, err := resource.ParseQuantity(strconv.Itoa(mincpu) + "m")
		if err != nil {
			e = multierror.Append(e, errors.Wrap(err, "Failed to parse mincpu"))
		}
		r.Requests[v1.ResourceCPU] = cpuRequest
	}

	if flags.IsSet(RUNTIME_MINMEMORY) {
		minmem := flags.Int(RUNTIME_MINMEMORY)
		memRequest, err := resource.ParseQuantity(strconv.Itoa(minmem) + "Mi")
		if err != nil {
			e = multierror.Append(e, errors.Wrap(err, "Failed to parse minmemory"))
		}
		r.Requests[v1.ResourceMemory] = memRequest
	}

	if flags.IsSet(RUNTIME_MAXCPU) {
		maxcpu := flags.Int(RUNTIME_MAXCPU)
		cpuLimit, err := resource.ParseQuantity(strconv.Itoa(maxcpu) + "m")
		if err != nil {
			e = multierror.Append(e, errors.Wrap(err, "Failed to parse maxcpu"))
		}
		r.Limits[v1.ResourceCPU] = cpuLimit
	}

	if flags.IsSet(RUNTIME_MAXMEMORY) {
		maxmem := flags.Int(RUNTIME_MAXMEMORY)
		memLimit, err := resource.ParseQuantity(strconv.Itoa(maxmem) + "Mi")
		if err != nil {
			e = multierror.Append(e, errors.Wrap(err, "Failed to parse maxmemory"))
		}
		r.Limits[v1.ResourceMemory] = memLimit
	}

	limitCPU := r.Limits[v1.ResourceCPU]
	requestCPU := r.Requests[v1.ResourceCPU]

	if limitCPU.IsZero() && !requestCPU.IsZero() {
		r.Limits[v1.ResourceCPU] = requestCPU
	} else if limitCPU.Cmp(requestCPU) < 0 {
		e = multierror.Append(e, fmt.Errorf("MinCPU (%v) cannot be greater than MaxCPU (%v)", requestCPU.String(), limitCPU.String()))
	}

	limitMem := r.Limits[v1.ResourceMemory]
	requestMem := r.Requests[v1.ResourceMemory]

	if limitMem.IsZero() && !requestMem.IsZero() {
		r.Limits[v1.ResourceMemory] = requestMem
	} else if limitMem.Cmp(requestMem) < 0 {
		e = multierror.Append(e, fmt.Errorf("MinMemory (%v) cannot be greater than MaxMemory (%v)", requestMem.String(), limitMem.String()))
	}

	if e.ErrorOrNil() != nil {
		return nil, e
	}

	return &v1.ResourceRequirements{
		Requests: r.Requests,
		Limits:   r.Limits,
	}, nil
}

func GetSpecDir(flags cli.Input) string {
	specDir := flags.String(SPEC_SPECDIR)
	if len(specDir) == 0 {
		specDir = "specs"
	}
	return specDir
}

// GetMetadata returns a pointer to ObjectMeta which initialized with command line input.
func GetMetadata(flags cli.Input) (*metav1.ObjectMeta, error) {
	name := flags.String(RESOURCE_NAME)
	if len(name) == 0 {
		return nil, errors.New("Need a resource name, use --name.")
	}

	ns := flags.String(ENVIRONMENT_NAMESPACE)

	m := &metav1.ObjectMeta{
		Name:      name,
		Namespace: ns,
	}

	return m, nil
}
