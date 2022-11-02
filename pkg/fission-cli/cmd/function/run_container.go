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
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type RunContainerSubCommand struct {
	cmd.CommandActioner
	function *fv1.Function
	specFile string
}

func RunContainer(input cli.Input) error {
	return (&RunContainerSubCommand{}).do(input)
}

func (opts *RunContainerSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *RunContainerSubCommand) complete(input cli.Input) error {
	fnName := input.String(flagkey.FnName)

	_, fnNamespace, err := util.GetResourceNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return errors.Wrap(err, "error in running container for function ")
	}

	// user wants a spec, create a yaml file with package and function
	toSpec := false
	if input.Bool(flagkey.SpecSave) {
		toSpec = true
		opts.specFile = fmt.Sprintf("function-%v.yaml", fnName)
	}

	if !toSpec {
		// check for unique function names within a namespace
		fn, err := opts.Client().FissionClientSet.CoreV1().Functions(fnNamespace).Get(input.Context(), input.String(flagkey.FnName), metav1.GetOptions{})

		if err != nil && !kerrors.IsNotFound(err) {
			return err
		} else if fn.Name != "" && fn.Namespace != "" {
			return errors.New("a function with the same name already exists")
		}
	}

	fnTimeout := input.Int(flagkey.FnExecutionTimeout)
	if fnTimeout <= 0 {
		return errors.Errorf("--%v must be greater than 0", flagkey.FnExecutionTimeout)
	}

	fnIdleTimeout := input.Int(flagkey.FnIdleTimeout)

	secretNames := input.StringSlice(flagkey.FnSecret)
	cfgMapNames := input.StringSlice(flagkey.FnCfgMap)

	es, err := getExecutionStrategy(fv1.ExecutorTypeContainer, input)
	if err != nil {
		return err
	}
	invokeStrategy := &fv1.InvokeStrategy{
		ExecutionStrategy: *es,
		StrategyType:      fv1.StrategyTypeExecution,
	}
	resourceReq, err := util.GetResourceReqs(input, &apiv1.ResourceRequirements{})
	if err != nil {
		return err
	}

	fnGracePeriod := input.Int64(flagkey.FnGracePeriod)
	if fnGracePeriod < 0 {
		console.Warn("grace period must be a non-negative integer, using default value (6 mins)")
	}

	var imageName string
	var port int
	var command, args string

	imageName = input.String(flagkey.FnImageName)
	if imageName == "" {
		return errors.New("need --image argument")
	}
	port = input.Int(flagkey.FnPort)
	command = input.String(flagkey.FnCommand)
	args = input.String(flagkey.FnArgs)

	var secrets []fv1.SecretReference
	var cfgmaps []fv1.ConfigMapReference

	if len(secretNames) > 0 {
		// check the referenced secret is in the same ns as the function, if not give a warning.
		if !toSpec { // TODO: workaround in order not to block users from creating function spec, remove it.
			for _, secretName := range secretNames {
				// TODO: discuss if this is fine or should we have a wrapper over kclient interface
				err := util.SecretExists(input.Context(), &metav1.ObjectMeta{Namespace: fnNamespace, Name: secretName}, opts.Client().KubernetesClient)
				if err != nil {
					if kerrors.IsNotFound(err) {
						console.Warn(fmt.Sprintf("Secret %s not found in Namespace: %s. Secret needs to be present in the same namespace as function", secretName, fnNamespace))
					} else {
						return errors.Wrapf(err, "error checking secret %s", secretName)
					}
				}
			}
		}
		for _, secretName := range secretNames {
			newSecret := fv1.SecretReference{
				Name:      secretName,
				Namespace: fnNamespace,
			}
			secrets = append(secrets, newSecret)
		}
	}

	if len(cfgMapNames) > 0 {
		// check the referenced cfgmap is in the same ns as the function, if not give a warning.
		if !toSpec {
			for _, cfgMapName := range cfgMapNames {
				err := util.ConfigMapExists(input.Context(), &metav1.ObjectMeta{Namespace: fnNamespace, Name: cfgMapName}, opts.Client().KubernetesClient)

				if err != nil {
					if kerrors.IsNotFound(err) {
						console.Warn(fmt.Sprintf("ConfigMap %s not found in Namespace: %s. ConfigMap needs to be present in the same namespace as function", cfgMapName, fnNamespace))
					} else {
						return errors.Wrapf(err, "error checking configmap %s", cfgMapName)
					}
				}
			}
		}
		for _, cfgMapName := range cfgMapNames {
			newCfgMap := fv1.ConfigMapReference{
				Name:      cfgMapName,
				Namespace: fnNamespace,
			}
			cfgmaps = append(cfgmaps, newCfgMap)
		}
	}

	opts.function = &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fnName,
			Namespace: fnNamespace,
		},
		Spec: fv1.FunctionSpec{
			Secrets:         secrets,
			ConfigMaps:      cfgmaps,
			Resources:       *resourceReq,
			InvokeStrategy:  *invokeStrategy,
			FunctionTimeout: fnTimeout,
			IdleTimeout:     &fnIdleTimeout,
		},
	}

	err = util.ApplyLabelsAndAnnotations(input, &opts.function.ObjectMeta)
	if err != nil {
		return err
	}

	container := &apiv1.Container{
		Name:  fnName,
		Image: imageName,
		Ports: []apiv1.ContainerPort{
			{
				Name:          "http-env",
				ContainerPort: int32(port),
			},
		},
	}
	if command != "" {
		container.Command = strings.Split(command, " ")
	}
	if args != "" {
		container.Args = strings.Split(args, " ")
	}

	opts.function.Spec.PodSpec = &apiv1.PodSpec{
		Containers:                    []apiv1.Container{*container},
		TerminationGracePeriodSeconds: &fnGracePeriod,
	}

	return nil
}

// run write the resource to a spec file or create a fission CRD with remote fission server.
// It also prints warning/error if necessary.
func (opts *RunContainerSubCommand) run(input cli.Input) error {
	// if we're writing a spec, don't create the function
	// save to spec file or display the spec to console
	if input.Bool(flagkey.SpecDry) {
		return spec.SpecDry(*opts.function)
	}

	if input.Bool(flagkey.SpecSave) {
		err := spec.SpecSave(*opts.function, opts.specFile)
		if err != nil {
			return errors.Wrap(err, "error saving function spec")
		}
		return nil
	}

	_, err := opts.Client().FissionClientSet.CoreV1().Functions(opts.function.ObjectMeta.Namespace).Create(input.Context(), opts.function, metav1.CreateOptions{})
	if err != nil {
		return errors.Wrap(err, "error creating function")
	}

	fmt.Printf("function '%v' created\n", opts.function.ObjectMeta.Name)
	return nil
}
