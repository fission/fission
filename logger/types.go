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

package logger

import "sync"

type (
	LogRequest struct {
		Namespace   string `json:"namespace"`
		Pod         string `json:"pod"`
		Container   string `json:"container"`
		FuncName    string `json:"funcname"`
		FuncUid     string `json:"funcuid"`
		ContainerID string `json:"-"`
	}

	logRequestTracker struct {
		sync.RWMutex
		logMap map[string]LogRequest
	}
)
