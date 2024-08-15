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

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
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

func (opts *UpdateSubCommand) complete(input cli.Input) (err error) {
	_, namespace, err := opts.GetResourceNamespace(input, flagkey.NamespaceTrigger)
	if err != nil {
		return errors.Wrap(err, "error in deleting function ")
	}

	mqt, err := opts.Client().FissionClientSet.CoreV1().MessageQueueTriggers(namespace).Get(input.Context(), input.String(flagkey.MqtName), metav1.GetOptions{})
	if err != nil {
		return errors.Wrap(err, "error getting message queue trigger")
	}

	topic := input.String(flagkey.MqtTopic)
	respTopic := input.String(flagkey.MqtRespTopic)
	errorTopic := input.String(flagkey.MqtErrorTopic)
	maxRetries := input.Int(flagkey.MqtMaxRetries)
	fnName := input.String(flagkey.MqtFnName)
	contentType := input.String(flagkey.MqtMsgContentType)
	pollingInterval := int32(input.Int(flagkey.MqtPollingInterval))
	cooldownPeriod := int32(input.Int(flagkey.MqtCooldownPeriod))
	minReplicaCount := int32(input.Int(flagkey.MqtMinReplicaCount))
	maxReplicaCount := int32(input.Int(flagkey.MqtMaxReplicaCount))
	metadataParams := input.StringSlice(flagkey.MqtMetadata)
	secret := input.String(flagkey.MqtSecret)
	mqtKind := input.String(flagkey.MqtKind)
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
	if input.IsSet(flagkey.MqtMaxRetries) {
		mqt.Spec.MaxRetries = maxRetries
		updated = true
	}
	if len(fnName) > 0 {
		functionList := []string{fnName}
		err := util.CheckFunctionExistence(input.Context(), opts.Client(), functionList, namespace)
		if err != nil {
			console.Warn(err.Error())
		}

		mqt.Spec.FunctionReference.Name = fnName
		updated = true
	}
	if input.IsSet(flagkey.MqtMsgContentType) {
		mqt.Spec.ContentType = contentType
		updated = true
	}
	if input.IsSet(flagkey.MqtPollingInterval) {
		mqt.Spec.PollingInterval = &pollingInterval
		updated = true
	}
	if input.IsSet(flagkey.MqtCooldownPeriod) {
		mqt.Spec.CooldownPeriod = &cooldownPeriod
		updated = true
	}
	if input.IsSet(flagkey.MqtMinReplicaCount) {
		mqt.Spec.MinReplicaCount = &minReplicaCount
		updated = true
	}
	if input.IsSet(flagkey.MqtMaxReplicaCount) {
		mqt.Spec.MaxReplicaCount = &maxReplicaCount
		updated = true
	}

	if input.IsSet(flagkey.MqtMetadata) {
		_ = util.UpdateMapFromStringSlice(&mqt.Spec.Metadata, metadataParams)
		updated = true
	}
	if input.IsSet(flagkey.MqtSecret) {
		mqt.Spec.Secret = secret
		updated = true
	}

	if input.IsSet(flagkey.MqtKind) {
		mqt.Spec.MqtKind = mqtKind
		updated = true
	}

	if !updated {
		return errors.New("Nothing changed, see 'help' for more details")
	}
	opts.trigger = mqt

	return nil
}

func (opts *UpdateSubCommand) run(input cli.Input) error {
	if input.Bool(flagkey.SpecSave) {
		err := opts.trigger.Validate()
		if err != nil {
			return fv1.AggregateValidationErrors("MessageQueueTrigger", err)
		}
		specFile := fmt.Sprintf("mqtrigger-%s.yaml", opts.trigger.ObjectMeta.Name)
		err = spec.SpecSave(*opts.trigger, specFile, true)
		if err != nil {
			return errors.Wrap(err, "error saving message queue trigger spec")
		}
		return nil
	}
	_, err := opts.Client().FissionClientSet.CoreV1().MessageQueueTriggers(opts.trigger.ObjectMeta.Namespace).Update(input.Context(), opts.trigger, metav1.UpdateOptions{})
	if err != nil {
		return errors.Wrap(err, "error updating message queue trigger")
	}

	fmt.Printf("message queue trigger '%v' updated\n", opts.trigger.ObjectMeta.Name)
	return nil
}
