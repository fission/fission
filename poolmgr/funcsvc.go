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

package poolmgr

import (
	"time"

	"github.com/fission/fission"
)

type funcSvc struct {
	function    *fission.Metadata    // function this pod/service is for
	environment *fission.Environment // env it was obtained from
	podAddress  string               // Host:Port or IP:Port that the pod can be reached at directly
	svcAddress  string               // Host:Port or IP:Port that the K8s service can be reached at
	podName     string               // pod name (within the function namespace)

	ctime time.Time
	atime time.Time
}

func (f *funcSvc) getAddress() string {
	svcCreationTime := 5 * time.Second
	if time.Now().Sub(f.ctime) > svcCreationTime {
		return f.svcAddress
	} else {
		return f.podAddress
	}
}
