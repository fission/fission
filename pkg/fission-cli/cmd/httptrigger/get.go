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

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
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

func (opts *GetSubCommand) run(input cli.Input) error {
	m := &metav1.ObjectMeta{
		Name:      input.String(flagkey.HtName),
		Namespace: input.String(flagkey.NamespaceFunction),
	}
	ht, err := opts.Client().V1().HTTPTrigger().Get(m)
	if err != nil {
		return errors.Wrap(err, "error getting http trigger")
	}

	printHtSummary([]fv1.HTTPTrigger{*ht})

	return nil
}

func printHtSummary(triggers []fv1.HTTPTrigger) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n", "NAME", "METHOD", "URL", "FUNCTION(s)", "INGRESS", "HOST", "PATH", "TLS", "ANNOTATIONS")
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

		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\t%v\n",
			trigger.Metadata.Name, trigger.Spec.Method, trigger.Spec.RelativeURL, function, trigger.Spec.CreateIngress, host, path, trigger.Spec.IngressConfig.TLS, ann)
	}
	w.Flush()
}
