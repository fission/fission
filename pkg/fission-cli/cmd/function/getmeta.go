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
	"os"
	"text/tabwriter"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type GetMetaSubCommand struct {
	cmd.CommandActioner
}

func GetMeta(input cli.Input) error {
	return (&GetMetaSubCommand{}).do(input)
}

func (opts *GetMetaSubCommand) do(input cli.Input) error {
	fnName := input.Args(0)
	fn, err := opts.Client().V1().Function().Get(&metav1.ObjectMeta{
		Name:      fnName,
		Namespace: input.String(flagkey.NamespaceFunction),
	})
	if err != nil {
		return errors.Wrap(err, "error getting function")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\n", "NAME", "ENV")
	fmt.Fprintf(w, "%v\t%v\n", fn.ObjectMeta.Name, fn.Spec.Environment.Name)
	w.Flush()

	return nil
}
