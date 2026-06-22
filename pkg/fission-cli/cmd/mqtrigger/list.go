// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

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
	opts.namespace, err = opts.ResolveNamespace(input)
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
