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
	"os"
	"text/tabwriter"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
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
	_, opts.namespace, err = util.GetResourceNamespace(input, flagkey.NamespaceTrigger)
	if err != nil {
		return errors.Wrap(err, "error in deleting function ")
	}
	return nil
}

func (opts *ListSubCommand) run(input cli.Input) (err error) {

	var mqts *v1.MessageQueueTriggerList
	if input.Bool(flagkey.AllNamespaces) {
		// TODO: mqtype is ignored as of now in the controller server. I am ignoring it too. This won't create conflict with current implementation.
		mqts, err = opts.Client().FissionClientSet.CoreV1().MessageQueueTriggers(metav1.NamespaceAll).List(input.Context(), metav1.ListOptions{})
	} else {
		mqts, err = opts.Client().FissionClientSet.CoreV1().MessageQueueTriggers(opts.namespace).List(input.Context(), metav1.ListOptions{})
	}
	if err != nil {
		return errors.Wrap(err, "error listing message queue triggers")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)

	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
		"NAME", "FUNCTION_NAME", "MESSAGE_QUEUE_TYPE", "TOPIC", "RESPONSE_TOPIC", "ERROR_TOPIC", "MAX_RETRIES", "PUB_MSG_CONTENT_TYPE", "NAMESPACE")
	for _, mqt := range mqts.Items {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
			mqt.ObjectMeta.Name, mqt.Spec.FunctionReference.Name, mqt.Spec.MessageQueueType, mqt.Spec.Topic, mqt.Spec.ResponseTopic, mqt.Spec.ErrorTopic, mqt.Spec.MaxRetries, mqt.Spec.ContentType, mqt.ObjectMeta.Namespace)
	}
	w.Flush()

	return nil
}
