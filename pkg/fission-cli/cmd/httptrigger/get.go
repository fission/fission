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
	"os"
	"strings"
	"text/tabwriter"

	"github.com/pkg/errors"
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
	_, namespace, err := util.GetResourceNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return errors.Wrap(err, "error in deleting function ")
	}

	ht, err := opts.Client().FissionClientSet.CoreV1().HTTPTriggers(namespace).Get(input.Context(), input.String(flagkey.HtName), metav1.GetOptions{})
	if err != nil {
		return errors.Wrap(err, "error getting http trigger")
	}

	printHtSummary([]fv1.HTTPTrigger{*ht})

	return nil
}

func printHtSummary(triggers []fv1.HTTPTrigger) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "METHOD", "URL", "FUNCTION(s)", "INGRESS", "HOST", "PATH", "TLS", "ANNOTATIONS", "NAMESPACE")
	for _, trigger := range triggers {
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
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
			trigger.ObjectMeta.Name, methods, trigger.Spec.RelativeURL, function, trigger.Spec.CreateIngress, host, path, trigger.Spec.IngressConfig.TLS, ann, trigger.ObjectMeta.Namespace)
	}
	w.Flush()
}
