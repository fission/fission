// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	_package "github.com/fission/fission/pkg/fission-cli/cmd/package"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type UpdateSubCommand struct {
	cmd.CommandActioner
	function *fv1.Function
	specFile string
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
	fnName := input.String(flagkey.FnName)
	_, fnNamespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in updating function : %w", err)
	}
	if input.Bool(flagkey.SpecSave) {
		opts.specFile = fmt.Sprintf("function-%s.yaml", fnName)
	}

	function, err := opts.Client().FissionClientSet.CoreV1().Functions(fnNamespace).Get(input.Context(), input.String(flagkey.FnName), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read function '%v': %w", fnName, err)
	}

	envName := input.String(flagkey.FnEnvironmentName)
	// if the new env specified is the same as the old one, no need to update package
	// same is true for all update parameters, but, for now, we don't check all of them - because, its ok to
	// re-write the object with same old values, we just end up getting a new resource version for the object.
	if len(envName) > 0 && envName == function.Spec.Environment.Name {
		envName = ""
	}

	pkgName := input.String(flagkey.FnPackageName)
	entrypoint := input.String(flagkey.FnEntrypoint)

	secretNames := input.StringSlice(flagkey.FnSecret)
	cfgMapNames := input.StringSlice(flagkey.FnCfgMap)

	// Only overwrite secret/configmap refs when the flag was provided, so an
	// update that doesn't mention them leaves the existing refs intact. Missing
	// resources warn but don't abort (the update path is lenient).
	if len(secretNames) > 0 {
		secrets, err := util.ResolveSecretReferences(input.Context(), opts.Client().KubernetesClient, secretNames, fnNamespace, true, false)
		if err != nil {
			return err
		}
		function.Spec.Secrets = secrets
	}

	if len(cfgMapNames) > 0 {
		configMaps, err := util.ResolveConfigMapReferences(input.Context(), opts.Client().KubernetesClient, cfgMapNames, fnNamespace, true, false)
		if err != nil {
			return err
		}
		function.Spec.ConfigMaps = configMaps
	}

	if len(envName) > 0 {
		function.Spec.Environment.Name = envName
	}

	if len(entrypoint) > 0 {
		function.Spec.Package.FunctionName = entrypoint
	}

	if input.IsSet(flagkey.FnExecutionTimeout) {
		fnTimeout := input.Int(flagkey.FnExecutionTimeout)
		if fnTimeout <= 0 {
			return fmt.Errorf("--%v must be greater than 0", flagkey.FnExecutionTimeout)
		}
		function.Spec.FunctionTimeout = fnTimeout
	}

	if input.IsSet(flagkey.FnIdleTimeout) {
		fnTimeout := input.Int(flagkey.FnIdleTimeout)
		function.Spec.IdleTimeout = &fnTimeout
	}

	// --streaming toggles the streaming config; when off it clears it (classic
	// path). The other --streaming* flags only take effect alongside --streaming.
	if input.IsSet(flagkey.FnStreaming) {
		function.Spec.Streaming = getStreamingConfig(input)
	}

	// --expose-as-mcp toggles the MCP tool config; when off it clears it. The
	// other --tool-* flags merge onto the existing config (only set fields change).
	if input.IsSet(flagkey.FnExposeAsMCP) {
		toolConfig, err := getToolConfig(input, function.Spec.Tool)
		if err != nil {
			return err
		}
		function.Spec.Tool = toolConfig
	}

	// --state toggles the keyed-state config; when off it clears it (opt-out
	// retains the keyspace data — see statesvc's reconciler). The other
	// --state-* flags merge onto the existing config (only set fields change).
	if input.IsSet(flagkey.FnState) {
		function.Spec.State = getStateConfig(input, function.Spec.State)
	}

	// The --async-* flags merge onto the existing InvocationConfig (only set fields
	// change); an empty --async-on-success/--async-on-failure (or -topic variant)
	// clears that destination.
	function.Spec.Invocation, err = getInvocationConfig(input, function.Spec.Invocation)
	if err != nil {
		return err
	}

	err = checkExecutorPoolManager(input, function.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType)
	if err != nil {
		return err
	}

	if input.IsSet(flagkey.FnConcurrency) {
		function.Spec.Concurrency = input.Int(flagkey.FnConcurrency)
	}

	if input.IsSet(flagkey.FnRequestsPerPod) {
		function.Spec.RequestsPerPod = input.Int(flagkey.FnRequestsPerPod)
	}

	if input.IsSet(flagkey.FnRetainPods) {
		function.Spec.RetainPods = input.Int(flagkey.FnRetainPods)
	}

	if input.IsSet(flagkey.FnOnceOnly) {
		function.Spec.OnceOnly = input.Bool(flagkey.FnOnceOnly)
	}
	if len(pkgName) == 0 {
		pkgName = function.Spec.Package.PackageRef.Name
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

	pkg, err := opts.Client().FissionClientSet.CoreV1().Packages(fnNamespace).Get(input.Context(), pkgName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read package '%v.%v'. Pkg should be present in the same ns as the function: %w", pkgName, fnNamespace, err)
	}

	forceUpdate := input.Bool(flagkey.PkgForce)

	fnList, err := _package.GetFunctionsByPackage(input.Context(), opts.Client(), pkg.Name, pkg.Namespace)
	if err != nil {
		return fmt.Errorf("error getting function list: %w", err)
	}

	if !forceUpdate && len(fnList) > 1 {
		return fmt.Errorf("package is used by multiple functions, use --%v to force update", flagkey.PkgForce)
	}

	newPkgMeta, err := _package.UpdatePackage(input, opts.Client(), opts.specFile, pkg)
	if err != nil {
		return fmt.Errorf("error updating package '%v': %w", pkgName, err)
	}

	// the package resource version of function has been changed,
	// we need to update function resource version to prevent conflict.
	// TODO: remove this block when deprecating pkg flags of function command.
	if pkg.ResourceVersion != newPkgMeta.ResourceVersion {
		var fns []fv1.Function
		// don't update the package resource version of the function we are currently
		// updating to prevent update conflict.
		for _, fn := range fnList {
			if fn.UID != function.UID {
				fns = append(fns, fn)
			}
		}
		err = _package.UpdateFunctionPackageResourceVersion(input.Context(), opts.Client(), newPkgMeta, fns...)
		if err != nil {
			return fmt.Errorf("error updating function package reference resource version: %w", err)
		}
	}

	// TODO : One corner case where user just updates the pkg reference with fnUpdate, but internally this new pkg reference
	// references a diff env than the spec

	// update function spec with new package metadata
	function.Spec.Package.PackageRef = fv1.PackageRef{
		Namespace:       newPkgMeta.Namespace,
		Name:            newPkgMeta.Name,
		ResourceVersion: newPkgMeta.ResourceVersion,
	}

	if function.Spec.Environment.Name != pkg.Spec.Environment.Name {
		console.Warn("Function's environment is different than package's environment, package's environment will be used for updating function")
		function.Spec.Environment.Name = pkg.Spec.Environment.Name
		function.Spec.Environment.Namespace = pkg.Spec.Environment.Namespace
	}

	opts.function = function

	err = util.ApplyLabelsAndAnnotations(input, &opts.function.ObjectMeta)
	if err != nil {
		return err
	}

	return nil
}

func (opts *UpdateSubCommand) run(input cli.Input) error {
	if input.Bool(flagkey.SpecSave) {
		err := opts.function.Validate()
		if err != nil {
			return fv1.AggregateValidationErrors("Function", err)
		}
		err = spec.SpecSave(*opts.function, opts.specFile, false)
		if err != nil {
			return fmt.Errorf("error saving function spec: %w", err)
		}
		return nil
	}
	// The package update already ran in complete(); only the function write is retried here.
	_, err := util.UpdateOnConflict(input.Context(),
		opts.Client().FissionClientSet.CoreV1().Functions(opts.function.Namespace),
		opts.function.Name, func(cur *fv1.Function) {
			cur.Spec = opts.function.Spec
			// Only overwrite metadata the command was actually given (see env update).
			if input.IsSet(flagkey.Labels) {
				cur.Labels = opts.function.Labels
			}
			if input.IsSet(flagkey.Annotation) {
				cur.Annotations = opts.function.Annotations
			}
		})
	if err != nil {
		return fmt.Errorf("error updating function: %w", err)
	}

	fmt.Printf("Function '%v' updated\n", opts.function.Name)
	return nil
}
