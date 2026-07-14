// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httptrigger

import (
	"fmt"
	"strings"

	"errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
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

func (opts *UpdateSubCommand) complete(input cli.Input) (err error) {
	htName := input.String(flagkey.HtName)

	_, triggerNamespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in updating HTTP trigger : %w", err)
	}

	ht, err := opts.Client().FissionClientSet.CoreV1().HTTPTriggers(triggerNamespace).Get(input.Context(), htName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting HTTP trigger: %w", err)
	}

	triggerUrl := input.String(flagkey.HtUrl)
	prefix := input.String(flagkey.HtPrefix)

	if triggerUrl != "" && prefix != "" {
		console.Warn("Prefix will take precedence over URL/RelativeURL")
	}

	if triggerUrl == "/" || prefix == "/" {
		return errors.New("url with only root path is not allowed")
	}
	if triggerUrl != "" && !strings.HasPrefix(triggerUrl, "/") {
		triggerUrl = "/" + triggerUrl
	}
	if prefix != "" && !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}

	ht.Spec.RelativeURL = triggerUrl
	ht.Spec.Prefix = &prefix

	if input.IsSet(flagkey.HtKeepPrefix) {
		ht.Spec.KeepPrefix = input.Bool(flagkey.HtKeepPrefix)
	}

	if input.IsSet(flagkey.HtInvocationMode) {
		ht.Spec.InvocationMode = input.String(flagkey.HtInvocationMode)
	}

	methods := input.StringSlice(flagkey.HtMethod)
	if len(methods) > 0 {
		for _, method := range methods {
			_, err := GetMethod(method)
			if err != nil {
				return err
			}
		}
		ht.Spec.Methods = methods
	}

	if input.IsSet(flagkey.HtFnName) {
		// get the functions and their weights if specified
		functionList := input.StringSlice(flagkey.HtFnName)
		err := util.CheckFunctionExistence(input.Context(), opts.Client(), functionList, triggerNamespace)
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
			return fmt.Errorf("error setting function weight: %w", err)
		}

		ht.Spec.FunctionReference = *functionRef
	}

	if input.IsSet(flagkey.HtIngress) {
		ht.Spec.CreateIngress = input.Bool(flagkey.HtIngress)
	}

	if input.IsSet(flagkey.HtHost) {
		ht.Spec.Host = input.String(flagkey.HtHost)
	}

	warnIngressDeprecated(input)

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
			return fmt.Errorf("error parsing ingress configuration: %w", err)
		}
		ht.Spec.IngressConfig = *ingress
	}

	if input.IsSet(flagkey.HtRouteProvider) || input.IsSet(flagkey.HtRouteHost) ||
		input.IsSet(flagkey.HtRoutePath) || input.IsSet(flagkey.HtRouteAnnotation) ||
		input.IsSet(flagkey.HtRouteTLS) || input.IsSet(flagkey.HtGateway) {
		// Merge into the existing RouteConfig so a partial update (e.g. only
		// --route-host) preserves previously-set fields, mirroring how the
		// ingress path threads the existing IngressConfig through GetIngressConfig.
		rc := &fv1.RouteConfig{}
		if ht.Spec.RouteConfig != nil {
			rc = ht.Spec.RouteConfig.DeepCopy()
		}
		if input.IsSet(flagkey.HtRouteProvider) {
			rc.Provider = fv1.RouteProviderType(input.String(flagkey.HtRouteProvider))
		}
		if input.IsSet(flagkey.HtRouteHost) {
			rc.Hostnames = input.StringSlice(flagkey.HtRouteHost)
		}
		if input.IsSet(flagkey.HtRoutePath) {
			rc.Path = input.String(flagkey.HtRoutePath)
		}
		if input.IsSet(flagkey.HtRouteTLS) {
			rc.TLS = input.String(flagkey.HtRouteTLS)
		}
		if input.IsSet(flagkey.HtRouteAnnotation) {
			// "-" clears all annotations, matching the ingress flag semantics.
			remove, anns, err := getIngressAnnotations(input.StringSlice(flagkey.HtRouteAnnotation))
			if err != nil {
				return fmt.Errorf("illegal route annotation: %w", err)
			}
			if remove {
				rc.Annotations = nil
			} else {
				rc.Annotations = anns
			}
		}
		if input.IsSet(flagkey.HtGateway) {
			parentRefs, err := parseGatewayParentRefs(input.StringSlice(flagkey.HtGateway))
			if err != nil {
				return err
			}
			rc.Gateway = &fv1.GatewayRouteConfig{ParentRefs: parentRefs}
		}
		if rc.Provider == "" {
			return fmt.Errorf("--%s is required to configure a route (one of %q, %q)",
				flagkey.HtRouteProvider, fv1.RouteProviderIngress, fv1.RouteProviderGateway)
		}
		if rc.Path == "" {
			if ht.Spec.Prefix != nil && *ht.Spec.Prefix != "" {
				rc.Path = *ht.Spec.Prefix
			} else {
				rc.Path = ht.Spec.RelativeURL
			}
		}
		ht.Spec.RouteConfig = rc
	}

	opts.trigger = ht

	return nil
}

func (opts *UpdateSubCommand) run(input cli.Input) error {
	if input.Bool(flagkey.SpecSave) {
		err := opts.trigger.Validate()
		if err != nil {
			return fv1.AggregateValidationErrors("HTTPTrigger", err)
		}
		specFile := fmt.Sprintf("route-%s.yaml", opts.trigger.Name)
		err = spec.SpecSave(*opts.trigger, specFile, true)
		if err != nil {
			return fmt.Errorf("error saving HTTP trigger spec: %w", err)
		}
		return nil
	}
	err := util.CheckHTTPTriggerDuplicates(input.Context(), opts.Client(), opts.trigger)
	if err != nil {
		return fmt.Errorf("error while creating HTTP Trigger: %w", err)
	}

	_, err = util.UpdateOnConflict(input.Context(),
		opts.Client().FissionClientSet.CoreV1().HTTPTriggers(opts.trigger.Namespace),
		opts.trigger.Name, func(cur *fv1.HTTPTrigger) { cur.Spec = opts.trigger.Spec })
	if err != nil {
		return fmt.Errorf("error updating the HTTP trigger: %w", err)
	}
	fmt.Printf("trigger '%v' updated\n", opts.trigger.Name)
	return nil
}
