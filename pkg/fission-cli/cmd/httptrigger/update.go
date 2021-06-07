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

package httptrigger

import (
	"fmt"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type UpdateSubCommand struct {
	cmd.CommandActioner
	trigger *fv1.HTTPTrigger
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
	htName := input.String(flagkey.HtName)
	triggerNamespace := input.String(flagkey.NamespaceTrigger)

	ht, err := opts.Client().V1().HTTPTrigger().Get(&metav1.ObjectMeta{
		Name:      htName,
		Namespace: triggerNamespace,
	})
	if err != nil {
		return errors.Wrap(err, "error getting HTTP trigger")
	}

	// TODO: add prefix support here
	if input.IsSet(flagkey.HtUrl) {
		ht.Spec.RelativeURL = input.String(flagkey.HtUrl)
	}
	if input.IsSet(flagkey.HtPrefix) {
		prefix := input.String(flagkey.HtPrefix)
		ht.Spec.Prefix = &prefix
	}

	if input.IsSet(flagkey.HtMethod) {
		ht.Spec.Method = input.String(flagkey.HtMethod)
	}

	if input.IsSet(flagkey.HtFnName) {
		// get the functions and their weights if specified
		functionList := input.StringSlice(flagkey.HtFnName)
		err := util.CheckFunctionExistence(opts.Client(), functionList, triggerNamespace)
		if err != nil {
			console.Warn(err.Error())
		}

		var functionWeightsList []int
		if input.IsSet(flagkey.HtFnWeight) {
			functionWeightsList = input.IntSlice(flagkey.HtFnWeight)
		}

		// set function reference
		functionRef, err := setHtFunctionRef(functionList, functionWeightsList)
		if err != nil {
			return errors.Wrap(err, "error setting function weight")
		}

		ht.Spec.FunctionReference = *functionRef
	}

	if input.IsSet(flagkey.HtIngress) {
		ht.Spec.CreateIngress = input.Bool(flagkey.HtIngress)
	}

	if input.IsSet(flagkey.HtHost) {
		ht.Spec.Host = input.String(flagkey.HtHost)
	}

	if input.IsSet(flagkey.HtIngressRule) || input.IsSet(flagkey.HtIngressAnnotation) || input.IsSet(flagkey.HtIngressTLS) {
		fallbackURL := ""
		if ht.Spec.Prefix != nil && *ht.Spec.Prefix != "" {
			fallbackURL = *ht.Spec.Prefix
		} else {
			fallbackURL = ht.Spec.RelativeURL
		}
		ingress, err := GetIngressConfig(
			input.StringSlice(flagkey.HtIngressAnnotation), input.String(flagkey.HtIngressRule),
			input.String(flagkey.HtIngressTLS), fallbackURL, &ht.Spec.IngressConfig)
		if err != nil {
			return errors.Wrap(err, "error parsing ingress configuration")
		}
		ht.Spec.IngressConfig = *ingress
	}

	opts.trigger = ht

	return nil
}

func (opts *UpdateSubCommand) run(input cli.Input) error {
	_, err := opts.Client().V1().HTTPTrigger().Update(opts.trigger)
	if err != nil {
		return errors.Wrap(err, "error updating the HTTP trigger")
	}
	fmt.Printf("trigger '%v' updated\n", opts.trigger.ObjectMeta.Name)
	return nil
}
