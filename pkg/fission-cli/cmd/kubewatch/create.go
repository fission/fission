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

package kubewatch

import (
	"fmt"

	"github.com/pkg/errors"
	uuid "github.com/satori/go.uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type CreateSubCommand struct {
	cmd.CommandActioner
	watcher *fv1.KubernetesWatchTrigger
}

func Create(input cli.Input) error {
	return (&CreateSubCommand{}).do(input)
}

func (opts *CreateSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *CreateSubCommand) complete(input cli.Input) error {
	watchName := input.String(flagkey.KwName)
	if len(watchName) == 0 {
		console.Warn(fmt.Sprintf("--%v will be soon marked as required flag, see 'help' for details", flagkey.MqtName))
		watchName = uuid.NewV4().String()
	}
	fnName := input.String(flagkey.KwFnName)
	fnNamespace := input.String(flagkey.NamespaceFunction)
	namespace := input.String(flagkey.KwNamespace)
	objType := input.String(flagkey.KwObjType)

	if input.Bool(flagkey.SpecSave) {
		specDir := util.GetSpecDir(input)
		fr, err := spec.ReadSpecs(specDir)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("error reading spec in '%v'", specDir))
		}

		exists, err := fr.ExistsInSpecs(fv1.Function{
			Metadata: metav1.ObjectMeta{
				Name:      fnName,
				Namespace: fnNamespace,
			},
		})
		if err != nil {
			return err
		}
		if !exists {
			console.Warn(fmt.Sprintf("KubernetesWatchTrigger '%v' references unknown Function '%v', please create it before applying spec",
				watchName, fnName))
		}
	}

	opts.watcher = &fv1.KubernetesWatchTrigger{
		Metadata: metav1.ObjectMeta{
			Name:      watchName,
			Namespace: fnNamespace,
		},
		Spec: fv1.KubernetesWatchTriggerSpec{
			Namespace: namespace,
			Type:      objType,
			//LabelSelector: labels,
			FunctionReference: fv1.FunctionReference{
				Name: fnName,
				Type: fv1.FunctionReferenceTypeFunctionName,
			},
		},
	}

	return nil
}

func (opts *CreateSubCommand) run(input cli.Input) error {
	// if we're writing a spec, don't call the API
	if input.Bool(flagkey.SpecSave) {
		specFile := fmt.Sprintf("kubewatch-%v.yaml", opts.watcher.Metadata.Name)
		err := spec.SpecSave(*opts.watcher, specFile)
		if err != nil {
			return errors.Wrap(err, "error creating kubewatch spec")
		}
		return nil
	}

	_, err := opts.Client().V1().KubeWatcher().Create(opts.watcher)
	if err != nil {
		return errors.Wrap(err, "error creating kubewatch")
	}

	fmt.Printf("trigger '%v' created\n", opts.watcher.Metadata.Name)
	return nil
}
