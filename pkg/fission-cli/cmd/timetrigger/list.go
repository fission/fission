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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type ListSubCommand struct {
	cmd.CommandActioner
}

func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) (err error) {
	ttNs, err := opts.ResolveNamespace(input, flagkey.NamespaceTrigger)
	if err != nil {
		return fmt.Errorf("error in listing time triggers: %w", err)
	}

	tts, err := opts.Client().FissionClientSet.CoreV1().TimeTriggers(ttNs).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list Time triggers: %w", err)
	}

	headers := []string{"NAME", "CRON", "FUNCTION_NAME", "METHOD", "SUBPATH", "READY"}
	util.PrintItems(headers, tts.Items, func(tt fv1.TimeTrigger) []string {
		return []string{
			tt.Name, tt.Spec.Cron, tt.Spec.Name, tt.Spec.Method, tt.Spec.Subpath,
			util.ConditionStatus(tt.Status.Conditions, fv1.TimeTriggerConditionReady),
		}
	})

	return nil
}
