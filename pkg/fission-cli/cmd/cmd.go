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
	"os"
	"sync"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
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

func (c *CommandActioner) GetResourceNamespace(input cli.Input, deprecatedFlag string) (namespace, currentNS string, err error) {
	namespace = input.String(deprecatedFlag)
	currentNS = namespace

	if input.String(flagkey.Namespace) != "" {
		namespace = input.String(flagkey.Namespace)
		currentNS = namespace
		console.Verbose(2, "Namespace for resource %s ", currentNS)
		return namespace, currentNS, err
	}

	if namespace == "" {
		if os.Getenv("FISSION_DEFAULT_NAMESPACE") != "" {
			currentNS = os.Getenv("FISSION_DEFAULT_NAMESPACE")
		} else {
			currentNS = c.Client().Namespace
			return namespace, currentNS, err
		}
	}

	console.Verbose(2, "Namespace for resource %s ", currentNS)
	return namespace, currentNS, nil
}
