// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httptrigger

import (
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type GetSubCommand struct {
	cmd.CommandActioner
}

func Get(input cli.Input) error {
	return (&GetSubCommand{}).do(input)
}

func (opts *GetSubCommand) do(input cli.Input) error {
	return opts.run(input)
}

func (opts *GetSubCommand) run(input cli.Input) (err error) {
	_, namespace, err := opts.GetResourceNamespace(input, flagkey.NamespaceTrigger)
	if err != nil {
		return fmt.Errorf("error in getting HTTP trigger: %w", err)
	}

	ht, err := opts.Client().FissionClientSet.CoreV1().HTTPTriggers(namespace).Get(input.Context(), input.String(flagkey.HtName), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting http trigger: %w", err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}
	if handled, err := util.PrintStructured(format, ht); err != nil || handled {
		return err
	}

	// describe renders the plain table (no -o wide AGE column), consistent with
	// the other describe commands; json/yaml are handled above.
	if err := printHtSummary(util.OutputTable, []fv1.HTTPTrigger{*ht}); err != nil {
		return err
	}
	util.PrintConditions(ht.Status.Conditions)

	return nil
}

func printHtSummary(format util.OutputFormat, triggers []fv1.HTTPTrigger) error {
	headers := []string{"NAME", "METHOD", "URL", "FUNCTION(s)", "INGRESS", "HOST", "PATH", "TLS", "ANNOTATIONS", "READY", "NAMESPACE"}
	row := func(trigger fv1.HTTPTrigger) []string {
		function := ""
		if trigger.Spec.FunctionReference.Type == fv1.FunctionReferenceTypeFunctionName {
			function = trigger.Spec.FunctionReference.Name
		} else {
			for k, v := range trigger.Spec.FunctionReference.FunctionWeights {
				function += fmt.Sprintf("%s:%v ", k, v)
			}
		}

		host := trigger.Spec.Host
		if len(trigger.Spec.IngressConfig.Host) > 0 {
			host = trigger.Spec.IngressConfig.Host
		}
		path := trigger.Spec.RelativeURL
		if len(trigger.Spec.IngressConfig.Path) > 0 {
			path = trigger.Spec.IngressConfig.Path
		}

		var msg []string
		for k, v := range trigger.Spec.IngressConfig.Annotations {
			msg = append(msg, fmt.Sprintf("%v: %v", k, v))
		}
		ann := strings.Join(msg, ", ")

		methods := []string{}
		if len(trigger.Spec.Method) > 0 {
			methods = append(methods, trigger.Spec.Method)
		}
		if len(trigger.Spec.Methods) > 0 {
			methods = trigger.Spec.Methods
		}
		return []string{
			trigger.Name, fmt.Sprintf("%v", methods), trigger.Spec.RelativeURL, function,
			fmt.Sprintf("%v", trigger.Spec.CreateIngress), host, path, fmt.Sprintf("%v", trigger.Spec.IngressConfig.TLS), ann,
			util.ConditionStatus(trigger.Status.Conditions, fv1.HTTPTriggerConditionReady),
			trigger.Namespace,
		}
	}
	wideExtra := []string{"AGE"}
	wideRow := func(trigger fv1.HTTPTrigger) []string { return []string{util.AgeOf(trigger.CreationTimestamp)} }
	return util.PrintObjects(format, triggers, headers, row, wideExtra, wideRow)
}
