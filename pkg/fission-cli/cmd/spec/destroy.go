// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
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
		return fmt.Errorf("error reading specs: %w", err)
	}

	if !input.Bool(flagkey.ForceDelete) {
		err = opts.insertNSToResource(input, fr)
		if err != nil {
			return fmt.Errorf("error adding namespace: %w", err)
		}
	} else {
		// if force delete set to true we fetch all resources with our deployment ID and delete them
		// set desired state to nothing, but keep the UID so "apply" can find it
		emptyFr := FissionResources{}
		emptyFr.DeploymentConfig = fr.DeploymentConfig

		// "apply" the empty state
		err = forceDeleteResources(input.Context(), opts.Client(), &emptyFr)
		if err != nil {
			return fmt.Errorf("error deleting resources: %w", err)
		}
		return nil
	}
	forceDelete := input.Bool(flagkey.ForceDelete)
	err = deleteResources(input.Context(), opts.Client(), fr, forceDelete)
	if err != nil {
		return fmt.Errorf("error deleting resources: %w", err)
	}

	return nil
}

func forceDeleteResources(ctx context.Context, fclient cmd.Client, fr *FissionResources) error {

	var err error

	_, _, err = applyHTTPTriggers(ctx, fclient, fr, true, false, false)
	if err != nil {
		return fmt.Errorf("HTTPTrigger delete failed: %w", err)
	}

	_, _, err = applyKubernetesWatchTriggers(ctx, fclient, fr, true, false, false)
	if err != nil {
		return fmt.Errorf("kubernetesWatchTrigger delete failed: %w", err)
	}

	_, _, err = applyTimeTriggers(ctx, fclient, fr, true, false, false)
	if err != nil {
		return fmt.Errorf("timeTrigger delete failed: %w", err)
	}

	_, _, err = applyMessageQueueTriggers(ctx, fclient, fr, true, false, false)
	if err != nil {
		return fmt.Errorf("messageQueueTrigger delete failed: %w", err)
	}

	_, _, err = applyWorkflows(ctx, fclient, fr, true, false, false)
	if err != nil {
		return fmt.Errorf("workflow delete failed: %w", err)
	}

	_, _, err = applyFunctions(ctx, fclient, fr, true, false, false)
	if err != nil {
		return fmt.Errorf("function delete failed: %w", err)
	}

	_, _, err = applyPackages(ctx, fclient, fr, true, false, false)
	if err != nil {
		return fmt.Errorf("package delete failed: %w", err)
	}

	_, _, err = applyEnvironments(ctx, fclient, fr, true, false, false)
	if err != nil {
		return fmt.Errorf("environment delete failed: %w", err)
	}

	return nil
}

// insertNSToResource provides a namespace to all resource which don't have a namespace specified
// in resource
func (opts *DestroySubCommand) insertNSToResource(input cli.Input, fr *FissionResources) error {
	_, currentNS, err := opts.GetResourceNamespace(input)
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
	for i := range fr.Workflows {
		if fr.Workflows[i].Namespace == "" {
			fr.Workflows[i].Namespace = currentNS
		}
	}

	return nil
}

func deleteResources(ctx context.Context, fclient cmd.Client, fr *FissionResources, _ bool) error {
	c := fclient.FissionClientSet.CoreV1()

	// Delete in dependency order: triggers first, then functions, then the
	// packages/environments they depend on. A missing resource is treated as
	// already deleted.
	if err := destroyResources(ctx, fr.HttpTriggers, "httptrigger", func(ctx context.Context, ns, name string) error {
		return c.HTTPTriggers(ns).Delete(ctx, name, metav1.DeleteOptions{})
	}); err != nil {
		return fmt.Errorf("HTTPTrigger delete failed: %w", err)
	}
	if err := destroyResources(ctx, fr.KubernetesWatchTriggers, "watch", func(ctx context.Context, ns, name string) error {
		return c.KubernetesWatchTriggers(ns).Delete(ctx, name, metav1.DeleteOptions{})
	}); err != nil {
		return fmt.Errorf("kubernetesWatchTrigger delete failed: %w", err)
	}
	if err := destroyResources(ctx, fr.TimeTriggers, "time trigger", func(ctx context.Context, ns, name string) error {
		return c.TimeTriggers(ns).Delete(ctx, name, metav1.DeleteOptions{})
	}); err != nil {
		return fmt.Errorf("timeTrigger delete failed: %w", err)
	}
	if err := destroyResources(ctx, fr.MessageQueueTriggers, "message trigger", func(ctx context.Context, ns, name string) error {
		return c.MessageQueueTriggers(ns).Delete(ctx, name, metav1.DeleteOptions{})
	}); err != nil {
		return fmt.Errorf("messageQueueTrigger delete failed: %w", err)
	}
	if err := destroyResources(ctx, fr.Workflows, "workflow", func(ctx context.Context, ns, name string) error {
		return c.Workflows(ns).Delete(ctx, name, metav1.DeleteOptions{})
	}); err != nil {
		return fmt.Errorf("workflow delete failed: %w", err)
	}
	if err := destroyResources(ctx, fr.Functions, "function", func(ctx context.Context, ns, name string) error {
		return c.Functions(ns).Delete(ctx, name, metav1.DeleteOptions{})
	}); err != nil {
		return fmt.Errorf("function delete failed: %w", err)
	}
	if err := destroyResources(ctx, fr.Packages, "package", func(ctx context.Context, ns, name string) error {
		return c.Packages(ns).Delete(ctx, name, metav1.DeleteOptions{})
	}); err != nil {
		return fmt.Errorf("package delete failed: %w", err)
	}
	if err := destroyResources(ctx, fr.Environments, "environment", func(ctx context.Context, ns, name string) error {
		return c.Environments(ns).Delete(ctx, name, metav1.DeleteOptions{})
	}); err != nil {
		return fmt.Errorf("environment delete failed: %w", err)
	}

	return nil
}

// destroyResources deletes each spec resource of one kind, treating a
// "not found" as success. noun labels the kind in verbose logs.
func destroyResources[T any, PT Object[T]](ctx context.Context, items []T, noun string, del func(ctx context.Context, namespace, name string) error) error {
	for i := range items {
		o := PT(&items[i])
		err := del(ctx, o.GetNamespace(), o.GetName())
		if apierrors.IsNotFound(err) {
			console.Verbose(2, "could not delete %s: %s Namespace: %s", noun, o.GetName(), o.GetNamespace())
			continue
		} else if err != nil {
			return err
		}
		fmt.Printf("Deleted %s %s\n", o.GetObjectKind().GroupVersionKind().Kind, k8sCache.MetaObjectToName(o).String())
	}
	return nil
}
