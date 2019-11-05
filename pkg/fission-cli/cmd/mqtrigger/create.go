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
	uuid "github.com/satori/go.uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/types"
)

type CreateSubCommand struct {
	client  *client.Client
	trigger *fv1.MessageQueueTrigger
}

func Create(flags cli.Input) error {
	c, err := util.GetServer(flags)
	if err != nil {
		return err
	}
	opts := CreateSubCommand{
		client: c,
	}
	return opts.do(flags)
}

func (opts *CreateSubCommand) do(flags cli.Input) error {
	err := opts.complete(flags)
	if err != nil {
		return err
	}
	return opts.run(flags)
}

func (opts *CreateSubCommand) complete(flags cli.Input) error {
	mqtName := flags.String("name")
	if len(mqtName) == 0 {
		mqtName = uuid.NewV4().String()
	}
	fnName := flags.String("function")
	if len(fnName) == 0 {
		return errors.New("Need a function name to create a trigger, use --function")
	}
	fnNamespace := flags.String("fnNamespace")

	var mqType fv1.MessageQueueType
	switch flags.String("mqtype") {
	case "":
		mqType = types.MessageQueueTypeNats
	case types.MessageQueueTypeNats:
		mqType = types.MessageQueueTypeNats
	case types.MessageQueueTypeASQ:
		mqType = types.MessageQueueTypeASQ
	case types.MessageQueueTypeKafka:
		mqType = types.MessageQueueTypeKafka
	default:
		return errors.New("Unknown message queue type, currently only \"nats-streaming, azure-storage-queue, kafka \" is supported")
	}

	// TODO: check topic availability
	topic := flags.String("topic")
	if len(topic) == 0 {
		return errors.New("Topic cannot be empty")
	}
	respTopic := flags.String("resptopic")

	if topic == respTopic {
		// TODO maybe this should just be a warning, perhaps
		// allow it behind a --force flag
		return errors.New("Listen topic should not equal to response topic")
	}

	errorTopic := flags.String("errortopic")

	maxRetries := flags.Int("maxretries")

	if maxRetries < 0 {
		return errors.New("Maximum number of retries must be a natural number, default is 0")
	}

	contentType := flags.String("contenttype")
	if len(contentType) == 0 {
		contentType = "application/json"
	}

	err := checkMQTopicAvailability(mqType, topic, respTopic)
	if err != nil {
		return err
	}

	opts.trigger = &fv1.MessageQueueTrigger{
		Metadata: metav1.ObjectMeta{
			Name:      mqtName,
			Namespace: fnNamespace,
		},
		Spec: fv1.MessageQueueTriggerSpec{
			FunctionReference: fv1.FunctionReference{
				Type: types.FunctionReferenceTypeFunctionName,
				Name: fnName,
			},
			MessageQueueType: mqType,
			Topic:            topic,
			ResponseTopic:    respTopic,
			ErrorTopic:       errorTopic,
			MaxRetries:       maxRetries,
			ContentType:      contentType,
		},
	}

	return nil
}

func (opts *CreateSubCommand) run(flags cli.Input) error {
	// if we're writing a spec, don't call the API
	if flags.Bool("spec") {
		specFile := fmt.Sprintf("mqtrigger-%v.yaml", opts.trigger.Metadata.Name)
		err := spec.SpecSave(*opts.trigger, specFile)
		if err != nil {
			return errors.Wrap(err, "error creating message queue trigger spec")
		}
		return nil
	}

	_, err := opts.client.MessageQueueTriggerCreate(opts.trigger)
	if err != nil {
		return errors.Wrap(err, "create message queue trigger")
	}

	fmt.Printf("message queue trigger '%s' created\n", opts.trigger.Metadata.Name)
	return nil
}

func checkMQTopicAvailability(mqType fv1.MessageQueueType, topics ...string) error {
	for _, t := range topics {
		if len(t) > 0 && !fv1.IsTopicValid(mqType, t) {
			return errors.Errorf("Invalid topic for %s: %s", mqType, t)
		}
	}
	return nil
}
