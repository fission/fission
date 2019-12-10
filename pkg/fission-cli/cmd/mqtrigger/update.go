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

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type UpdateSubCommand struct {
	cmd.CommandActioner
	trigger *fv1.MessageQueueTrigger
}

func Update(input cli.Input) error {
	return (&UpdateSubCommand{}).do(input)
}

func (opts *UpdateSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *UpdateSubCommand) complete(input cli.Input) error {
	mqt, err := opts.Client().V1().MessageQueueTrigger().Get(&metav1.ObjectMeta{
		Name:      input.String(flagkey.MqtName),
		Namespace: input.String(flagkey.NamespaceTrigger),
	})
	if err != nil {
		return errors.Wrap(err, "error getting message queue trigger")
	}

	topic := input.String(flagkey.MqtTopic)
	respTopic := input.String(flagkey.MqtRespTopic)
	errorTopic := input.String(flagkey.MqtErrorTopic)
	maxRetries := input.Int(flagkey.MqtMaxRetries)
	fnName := input.String(flagkey.MqtFnName)
	contentType := input.String(flagkey.MqtMsgContentType)

	// TODO : Find out if we can make a call to checkIfFunctionExists, in the same ns more importantly.

	err = checkMQTopicAvailability(mqt.Spec.MessageQueueType, topic, respTopic)
	if err != nil {
		return err
	}

	updated := false
	if len(topic) > 0 {
		mqt.Spec.Topic = topic
		updated = true
	}
	if len(respTopic) > 0 {
		mqt.Spec.ResponseTopic = respTopic
		updated = true
	}
	if len(errorTopic) > 0 {
		mqt.Spec.ErrorTopic = errorTopic
		updated = true
	}
	if maxRetries > -1 {
		mqt.Spec.MaxRetries = maxRetries
		updated = true
	}
	if len(fnName) > 0 {
		mqt.Spec.FunctionReference.Name = fnName
		updated = true
	}
	if len(contentType) > 0 {
		mqt.Spec.ContentType = contentType
		updated = true
	}

	if !updated {
		return errors.New("Nothing changed, see 'help' for more details")
	}
	opts.trigger = mqt

	return nil
}

func (opts *UpdateSubCommand) run(input cli.Input) error {
	_, err := opts.Client().V1().MessageQueueTrigger().Update(opts.trigger)
	if err != nil {
		return errors.Wrap(err, "error updating message queue trigger")
	}

	fmt.Printf("message queue trigger '%v' updated\n", opts.trigger.Metadata.Name)
	return nil
}
