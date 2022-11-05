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
	"sync"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
)

type (
	CommandAction   func(input cli.Input) error
	CommandActioner struct{}
)

var (
	once          = sync.Once{}
	defaultClient Client
)

func SetClientset(client Client) {
	once.Do(func() {
		defaultClient = client
	})
}

func (c *CommandActioner) Client() Client {
	return defaultClient
}
