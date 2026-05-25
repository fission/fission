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

package mqtrigger

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
	namespace string
}

func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *ListSubCommand) complete(input cli.Input) (err error) {
	opts.namespace, err = opts.ResolveNamespace(input, flagkey.NamespaceTrigger)
	if err != nil {
		return fmt.Errorf("error in listing message queue triggers: %w", err)
	}
	return nil
}

func (opts *ListSubCommand) run(input cli.Input) (err error) {
	mqts, err := opts.Client().FissionClientSet.CoreV1().MessageQueueTriggers(opts.namespace).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error listing message queue triggers: %w", err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	headers := []string{"NAME", "FUNCTION_NAME", "MESSAGE_QUEUE_TYPE", "TOPIC", "RESPONSE_TOPIC", "ERROR_TOPIC", "MAX_RETRIES", "PUB_MSG_CONTENT_TYPE", "READY", "NAMESPACE"}
	row := func(mqt fv1.MessageQueueTrigger) []string {
		return []string{
			mqt.Name, mqt.Spec.FunctionReference.Name, string(mqt.Spec.MessageQueueType), mqt.Spec.Topic, mqt.Spec.ResponseTopic, mqt.Spec.ErrorTopic,
			fmt.Sprintf("%v", mqt.Spec.MaxRetries), mqt.Spec.ContentType,
			util.ConditionStatus(mqt.Status.Conditions, fv1.MessageQueueTriggerConditionReady),
			mqt.Namespace,
		}
	}
	wideExtra := []string{"AGE"}
	wideRow := func(mqt fv1.MessageQueueTrigger) []string { return []string{util.AgeOf(mqt.CreationTimestamp)} }

	return util.PrintObjects(format, mqts.Items, headers, row, wideExtra, wideRow)
}
