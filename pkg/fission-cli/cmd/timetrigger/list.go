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

package timetrigger

import (
	"fmt"
	"os"
	"text/tabwriter"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type ListSubCommand struct {
	cmd.CommandActioner
}

func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) (err error) {
	_, ttNs, err := opts.GetResourceNamespace(input, flagkey.NamespaceTrigger)
	if err != nil {
		return fmt.Errorf("error in deleting function : %w", err)
	}

	if input.Bool(flagkey.AllNamespaces) {
		ttNs = metav1.NamespaceAll
	}
	tts, err := opts.Client().FissionClientSet.CoreV1().TimeTriggers(ttNs).List(input.Context(), metav1.ListOptions{})

	if err != nil {
		return fmt.Errorf("list Time triggers: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)

	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n", "NAME", "CRON", "FUNCTION_NAME", "METHOD", "SUBPATH")
	for _, tt := range tts.Items {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n",
			tt.ObjectMeta.Name, tt.Spec.Cron, tt.Spec.Name, tt.Spec.Method, tt.Spec.Subpath)
	}
	w.Flush()

	return nil
}
