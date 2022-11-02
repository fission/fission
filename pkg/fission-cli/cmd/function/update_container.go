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

package function

import (
	"fmt"
	"strings"

	"github.com/pkg/errors"
	apiv1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type UpdateContainerSubCommand struct {
	cmd.CommandActioner
	function *fv1.Function
}

func UpdateContainer(input cli.Input) error {
	return (&UpdateContainerSubCommand{}).do(input)
}

func (opts *UpdateContainerSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *UpdateContainerSubCommand) complete(input cli.Input) error {
	fnName := input.String(flagkey.FnName)

	_, fnNamespace, err := util.GetResourceNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return errors.Wrap(err, "error in updating container for function ")
	}

	function, err := opts.Client().FissionClientSet.CoreV1().Functions(fnNamespace).Get(input.Context(), input.String(flagkey.FnName), metav1.GetOptions{})

	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("read function '%v'", fnName))
	}
	if fv1.ExecutorTypeContainer != function.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType {
		return fmt.Errorf("executor type for function is not %s", fv1.ExecutorTypeContainer)
	}

	imageName := input.String(flagkey.FnImageName)
	port := input.Int(flagkey.FnPort)
	command := input.String(flagkey.FnCommand)
	args := input.String(flagkey.FnArgs)

	secretNames := input.StringSlice(flagkey.FnSecret)
	cfgMapNames := input.StringSlice(flagkey.FnCfgMap)

	var secrets []fv1.SecretReference
	var configMaps []fv1.ConfigMapReference

	if len(secretNames) > 0 {

		// check that the referenced secret is in the same ns as the function, if not give a warning.
		for _, secretName := range secretNames {
			err := util.SecretExists(input.Context(), &metav1.ObjectMeta{Namespace: fnNamespace, Name: secretName}, opts.Client().KubernetesClient)
			if k8serrors.IsNotFound(err) {
				console.Warn(fmt.Sprintf("secret %s not found in Namespace: %s. Secret needs to be present in the same namespace as function", secretName, fnNamespace))
			}
		}

		for _, secretName := range secretNames {
			newSecret := fv1.SecretReference{
				Name:      secretName,
				Namespace: fnNamespace,
			}
			secrets = append(secrets, newSecret)
		}

		function.Spec.Secrets = secrets
	}

	if len(cfgMapNames) > 0 {

		// check that the referenced cfgmap is in the same ns as the function, if not give a warning.
		for _, cfgMapName := range cfgMapNames {
			err := util.ConfigMapExists(input.Context(), &metav1.ObjectMeta{Namespace: fnNamespace, Name: cfgMapName}, opts.Client().KubernetesClient)
			if k8serrors.IsNotFound(err) {
				console.Warn(fmt.Sprintf("ConfigMap %s not found in Namespace: %s. ConfigMap needs to be present in the same namespace as the function", cfgMapName, fnNamespace))
			}
		}

		for _, cfgMapName := range cfgMapNames {
			newCfgMap := fv1.ConfigMapReference{
				Name:      cfgMapName,
				Namespace: fnNamespace,
			}
			configMaps = append(configMaps, newCfgMap)
		}
		function.Spec.ConfigMaps = configMaps
	}

	if input.IsSet(flagkey.FnExecutionTimeout) {
		fnTimeout := input.Int(flagkey.FnExecutionTimeout)
		if fnTimeout <= 0 {
			return errors.Errorf("--%v must be greater than 0", flagkey.FnExecutionTimeout)
		}
		function.Spec.FunctionTimeout = fnTimeout
	}

	if input.IsSet(flagkey.FnIdleTimeout) {
		fnTimeout := input.Int(flagkey.FnIdleTimeout)
		function.Spec.IdleTimeout = &fnTimeout
	}

	strategy, err := getInvokeStrategy(input, &function.Spec.InvokeStrategy)
	if err != nil {
		return err
	}
	function.Spec.InvokeStrategy = *strategy

	resReqs, err := util.GetResourceReqs(input, &function.Spec.Resources)
	if err != nil {
		return err
	}

	function.Spec.Resources = *resReqs

	if len(function.Spec.PodSpec.Containers) > 1 {
		return errors.Errorf("function %s has more than one container, only one container is supported", fnName)
	}
	container := &function.Spec.PodSpec.Containers[0]
	if imageName != "" {
		container.Image = imageName
	}
	if port != 0 {
		if len(container.Ports) > 1 {
			return errors.Errorf("function %s has more than one port, only one port is supported", fnName)
		}
		container.Ports = []apiv1.ContainerPort{
			{
				Name:          "http-env",
				ContainerPort: int32(port),
			},
		}
	}
	if command != "" {
		container.Command = strings.Split(command, " ")
	}
	if args != "" {
		container.Args = strings.Split(args, " ")
	}

	function.Spec.Environment = fv1.EnvironmentReference{}
	function.Spec.Package = fv1.FunctionPackageRef{}

	opts.function = function

	err = util.ApplyLabelsAndAnnotations(input, &opts.function.ObjectMeta)
	if err != nil {
		return err
	}

	return nil
}

func (opts *UpdateContainerSubCommand) run(input cli.Input) error {
	_, err := opts.Client().FissionClientSet.CoreV1().Functions(opts.function.Namespace).Update(input.Context(), opts.function, metav1.UpdateOptions{})
	if err != nil {
		return errors.Wrap(err, "error updating function")
	}

	return nil
}
