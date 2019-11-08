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

package function

import (
	"fmt"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type DeleteSubCommand struct {
	client *client.Client
}

func Delete(input cli.Input) error {
	c, err := util.GetServer(input)
	if err != nil {
		return err
	}
	opts := DeleteSubCommand{
		client: c,
	}
	return opts.do(input)
}

func (opts *DeleteSubCommand) do(input cli.Input) error {
	m := &metav1.ObjectMeta{
		Name:      input.String(flagkey.FnName),
		Namespace: input.String(flagkey.NamespaceFunction),
	}

	err := opts.client.FunctionDelete(m)
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("delete function '%v'", m.Name))
	}

	fmt.Printf("function '%v' deleted\n", m.Name)
	return nil
}
