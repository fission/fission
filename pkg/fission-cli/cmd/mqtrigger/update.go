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

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type UpdateSubCommand struct {
	client  *client.Client
	trigger *fv1.MessageQueueTrigger
}

func Update(flags cli.Input) error {
	c, err := util.GetServer(flags)
	if err != nil {
		return err
	}
	opts := UpdateSubCommand{
		client: c,
	}
	return opts.do(flags)
}

func (opts *UpdateSubCommand) do(flags cli.Input) error {
	err := opts.complete(flags)
	if err != nil {
		return err
	}
	return opts.run(flags)
}

func (opts *UpdateSubCommand) complete(flags cli.Input) error {
	m, err := util.GetMetadata("name", "triggerns", flags)
	if err != nil {
		return err
	}

	mqt, err := opts.client.MessageQueueTriggerGet(m)
	if err != nil {
		return errors.Wrap(err, "error getting message queue trigger")
	}

	topic := flags.String("topic")
	respTopic := flags.String("resptopic")
	errorTopic := flags.String("errortopic")
	maxRetries := flags.Int("maxretries")
	fnName := flags.String("function")
	contentType := flags.String("contenttype")

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
		return errors.New("Nothing to update. Use --topic, --resptopic, --errortopic, --maxretries or --function.")
	}
	opts.trigger = mqt

	return nil
}

func (opts *UpdateSubCommand) run(flags cli.Input) error {
	_, err := opts.client.MessageQueueTriggerUpdate(opts.trigger)
	if err != nil {
		return errors.Wrap(err, "error updating message queue trigger")
	}

	fmt.Printf("message queue trigger '%v' updated\n", opts.trigger.Metadata.Name)
	return nil
}
