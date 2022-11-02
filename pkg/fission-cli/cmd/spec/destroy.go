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

package spec

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/utils"
)

type DestroySubCommand struct {
	cmd.CommandActioner
}

// Destroy destroys everything in the spec.
func Destroy(input cli.Input) error {
	return (&DestroySubCommand{}).do(input)
}

func (opts *DestroySubCommand) do(input cli.Input) error {
	return opts.run(input)
}

func (opts *DestroySubCommand) run(input cli.Input) error {
	// get specdir and specignore
	specDir := util.GetSpecDir(input)
	specIgnore := util.GetSpecIgnore(input)

	// read everything
	fr, err := ReadSpecs(specDir, specIgnore, false)
	if err != nil {
		return errors.Wrap(err, "error reading specs")
	}

	if !input.Bool(flagkey.ForceDelete) {
		err = opts.insertNSToResource(input, fr)
		if err != nil {
			return errors.Wrap(err, "error adding namespace")
		}
	} else {
		// if force delete set to true we fetch all resources with our deployment ID and delete them
		// set desired state to nothing, but keep the UID so "apply" can find it
		emptyFr := FissionResources{}
		emptyFr.DeploymentConfig = fr.DeploymentConfig

		// "apply" the empty state
		err = forceDeleteResources(input.Context(), opts.Client(), &emptyFr)
		if err != nil {
			return errors.Wrap(err, "error deleting resources")
		}
		return nil
	}
	forceDelete := input.Bool(flagkey.ForceDelete)
	err = deleteResources(input.Context(), opts.Client(), fr, forceDelete)
	if err != nil {
		return errors.Wrap(err, "error deleting resources")
	}

	return nil
}

func forceDeleteResources(ctx context.Context, fclient cmd.Client, fr *FissionResources) error {

	var err error

	_, _, err = applyHTTPTriggers(ctx, fclient, fr, true, false)
	if err != nil {
		return errors.Wrap(err, "HTTPTrigger delete failed")
	}

	_, _, err = applyKubernetesWatchTriggers(ctx, fclient, fr, true, false)
	if err != nil {
		return errors.Wrap(err, "KubernetesWatchTrigger delete failed")
	}

	_, _, err = applyTimeTriggers(ctx, fclient, fr, true, false)
	if err != nil {
		return errors.Wrap(err, "TimeTrigger delete failed")
	}

	_, _, err = applyMessageQueueTriggers(ctx, fclient, fr, true, false)
	if err != nil {
		return errors.Wrap(err, "MessageQueueTrigger delete failed")
	}

	_, _, err = applyFunctions(ctx, fclient, fr, true, false)
	if err != nil {
		return errors.Wrap(err, "function delete failed")
	}

	_, _, err = applyPackages(ctx, fclient, fr, true, false)
	if err != nil {
		return errors.Wrap(err, "package delete failed")
	}

	_, _, err = applyEnvironments(ctx, fclient, fr, true, false)
	if err != nil {
		return errors.Wrap(err, "environment delete failed")
	}

	return nil
}

// insertNSToResource provides a namespace to all resource which don't have a namespace specified
// in resource
func (opts *DestroySubCommand) insertNSToResource(input cli.Input, fr *FissionResources) error {

	result := utils.MultiErrorWithFormat()

	_, currentNS, err := util.GetResourceNamespace(input, flagkey.NamespaceEnvironment)
	if err != nil {
		return fv1.AggregateValidationErrors("Environment", err)
	}

	for i := range fr.Functions {
		if fr.Functions[i].Namespace == "" {
			fr.Functions[i].Namespace = currentNS
		}
	}
	for i := range fr.Environments {
		if fr.Environments[i].Namespace == "" {
			fr.Environments[i].Namespace = currentNS
		}
	}
	for i := range fr.Packages {
		if fr.Packages[i].Namespace == "" {
			fr.Packages[i].Namespace = currentNS
		}
	}
	for i := range fr.HttpTriggers {
		if fr.HttpTriggers[i].Namespace == "" {
			fr.HttpTriggers[i].Namespace = currentNS
		}
	}
	for i := range fr.MessageQueueTriggers {
		if fr.MessageQueueTriggers[i].Namespace == "" {
			fr.MessageQueueTriggers[i].Namespace = currentNS
		}
	}
	for i := range fr.TimeTriggers {
		if fr.TimeTriggers[i].Namespace == "" {
			fr.TimeTriggers[i].Namespace = currentNS
		}
	}
	for i := range fr.KubernetesWatchTriggers {
		if fr.KubernetesWatchTriggers[i].Namespace == "" {
			fr.KubernetesWatchTriggers[i].Namespace = currentNS
		}
	}

	return result.ErrorOrNil()
}

func deleteResources(ctx context.Context, fclient cmd.Client, fr *FissionResources, forceDelete bool) error {

	var err error

	err = destroyHTTPTriggers(ctx, fclient, fr)
	if err != nil {
		return errors.Wrap(err, "HTTPTrigger delete failed")
	}

	err = destroyKubernetesWatchTriggers(ctx, fclient, fr)
	if err != nil {
		return errors.Wrap(err, "KubernetesWatchTrigger delete failed")
	}

	err = destroyTimeTriggers(ctx, fclient, fr)
	if err != nil {
		return errors.Wrap(err, "TimeTrigger delete failed")
	}

	err = destroyMessageQueueTriggers(ctx, fclient, fr)
	if err != nil {
		return errors.Wrap(err, "MessageQueueTrigger delete failed")
	}

	err = destroyFunctions(ctx, fclient, fr)
	if err != nil {
		return errors.Wrap(err, "function delete failed")
	}

	err = destroyPackages(ctx, fclient, fr)
	if err != nil {
		return errors.Wrap(err, "package delete failed")
	}

	err = destroyEnvironments(ctx, fclient, fr)
	if err != nil {
		return errors.Wrap(err, "environment delete failed")
	}

	return nil
}

func destroyHTTPTriggers(ctx context.Context, fclient cmd.Client, fr *FissionResources) error {
	for _, o := range fr.HttpTriggers {
		err := fclient.FissionClientSet.CoreV1().HTTPTriggers(o.ObjectMeta.Namespace).Delete(ctx, o.ObjectMeta.Name, metav1.DeleteOptions{})
		if err != nil && strings.Contains(err.Error(), "not found") {
			console.Verbose(2, fmt.Sprintf("could not delete httptrigger: %s Namespace: %s", o.ObjectMeta.Name, o.ObjectMeta.Namespace))
			err = nil
			continue

		} else if err != nil {
			return err
		}
		fmt.Printf("Deleted %s %s/%s\n", o.TypeMeta.Kind, o.ObjectMeta.Namespace, o.ObjectMeta.Name)
	}
	return nil
}

func destroyKubernetesWatchTriggers(ctx context.Context, fclient cmd.Client, fr *FissionResources) error {

	for _, o := range fr.KubernetesWatchTriggers {
		err := fclient.FissionClientSet.CoreV1().KubernetesWatchTriggers(o.ObjectMeta.Namespace).Delete(ctx, o.ObjectMeta.Name, metav1.DeleteOptions{})
		if err != nil && strings.Contains(err.Error(), "not found") {
			console.Verbose(2, fmt.Sprintf("could not delete watch: %s Namespace: %s", o.ObjectMeta.Name, o.ObjectMeta.Namespace))
			err = nil
			continue

		} else if err != nil {
			return err
		}
		fmt.Printf("Deleted %s %s/%s\n", o.TypeMeta.Kind, o.ObjectMeta.Namespace, o.ObjectMeta.Name)
	}

	return nil
}

func destroyTimeTriggers(ctx context.Context, fclient cmd.Client, fr *FissionResources) error {

	for _, o := range fr.TimeTriggers {
		err := fclient.FissionClientSet.CoreV1().TimeTriggers(o.ObjectMeta.Namespace).Delete(ctx, o.ObjectMeta.Name, metav1.DeleteOptions{})
		if err != nil && strings.Contains(err.Error(), "not found") {
			console.Verbose(2, fmt.Sprintf("could not delete Time trigger: %s Namespace: %s", o.ObjectMeta.Name, o.ObjectMeta.Namespace))
			err = nil
			continue

		} else if err != nil {
			return err
		}
		fmt.Printf("Deleted %s %s/%s\n", o.TypeMeta.Kind, o.ObjectMeta.Namespace, o.ObjectMeta.Name)
	}

	return nil
}

func destroyMessageQueueTriggers(ctx context.Context, fclient cmd.Client, fr *FissionResources) error {

	for _, o := range fr.MessageQueueTriggers {
		err := fclient.FissionClientSet.CoreV1().MessageQueueTriggers(o.ObjectMeta.Namespace).Delete(ctx, o.ObjectMeta.Name, metav1.DeleteOptions{})
		if err != nil && strings.Contains(err.Error(), "not found") {
			console.Verbose(2, fmt.Sprintf("could not delete Message trigger: %s Namespace: %s", o.ObjectMeta.Name, o.ObjectMeta.Namespace))
			err = nil
			continue

		} else if err != nil {
			return err
		}
		fmt.Printf("Deleted %s %s/%s\n", o.TypeMeta.Kind, o.ObjectMeta.Namespace, o.ObjectMeta.Name)
	}

	return nil
}

func destroyFunctions(ctx context.Context, fclient cmd.Client, fr *FissionResources) error {

	for _, o := range fr.Functions {
		err := fclient.FissionClientSet.CoreV1().Functions(o.ObjectMeta.Namespace).Delete(ctx, o.ObjectMeta.Name, metav1.DeleteOptions{})
		if err != nil && strings.Contains(err.Error(), "not found") {
			console.Verbose(2, fmt.Sprintf("could not delete Functions: %s Namespace: %s", o.ObjectMeta.Name, o.ObjectMeta.Namespace))
			err = nil
			continue

		} else if err != nil {
			return err
		}
		fmt.Printf("Deleted %s %s/%s\n", o.TypeMeta.Kind, o.ObjectMeta.Namespace, o.ObjectMeta.Name)
	}

	return nil
}

func destroyPackages(ctx context.Context, fclient cmd.Client, fr *FissionResources) error {

	for _, o := range fr.Packages {
		err := fclient.FissionClientSet.CoreV1().Packages(o.ObjectMeta.Namespace).Delete(ctx, o.ObjectMeta.Name, metav1.DeleteOptions{})
		if err != nil && strings.Contains(err.Error(), "not found") {
			console.Verbose(2, fmt.Sprintf("could not delete Package: %s Namespace: %s", o.ObjectMeta.Name, o.ObjectMeta.Namespace))
			err = nil
			continue

		} else if err != nil {
			return err
		}
		fmt.Printf("Deleted %s %s/%s\n", o.TypeMeta.Kind, o.ObjectMeta.Namespace, o.ObjectMeta.Name)
	}

	return nil
}

func destroyEnvironments(ctx context.Context, fclient cmd.Client, fr *FissionResources) error {

	for _, o := range fr.Environments {
		err := fclient.FissionClientSet.CoreV1().Environments(o.ObjectMeta.Namespace).Delete(ctx, o.ObjectMeta.Name, metav1.DeleteOptions{})
		if err != nil && strings.Contains(err.Error(), "not found") {
			console.Verbose(2, fmt.Sprintf("could not delete Env: %s Namespace: %s", o.ObjectMeta.Name, o.ObjectMeta.Namespace))
			err = nil
			continue

		} else if err != nil {
			return err
		}
		fmt.Printf("Deleted %s %s/%s\n", o.TypeMeta.Kind, o.ObjectMeta.Namespace, o.ObjectMeta.Name)
	}

	return nil
}
