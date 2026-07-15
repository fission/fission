// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httptrigger

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/utils/uuid"
)

type CreateSubCommand struct {
	cmd.CommandActioner
	trigger *fv1.HTTPTrigger
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
	functionList := input.StringSlice(flagkey.HtFnName)
	functionWeightsList := input.IntSlice(flagkey.HtFnWeight)

	if len(functionList) == 0 {
		return errors.New("need a function name to create a trigger, use --function")
	}

	functionRef, err := setHtFunctionRef(functionList, functionWeightsList)
	if err != nil {
		return err
	}

	triggerName := input.String(flagkey.HtName)
	// just name triggers by uuid.
	if len(triggerName) == 0 {
		console.Warn(fmt.Sprintf("--%v will be soon marked as required flag, see 'help' for details", flagkey.HtName))
		triggerName = uuid.NewString()
	}

	userProvidedNS, fnNamespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in creating HTTP trigger : %w", err)
	}

	triggerUrl := input.String(flagkey.HtUrl)
	prefix := input.String(flagkey.HtPrefix)
	fallbackURL := ""

	if triggerUrl == "" && prefix == "" {
		return errors.New("you need to supply either Prefix or URL/RelativeURL")
	}

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
	if prefix != "" {
		fallbackURL = prefix
	} else {
		fallbackURL = triggerUrl
	}

	methods := input.StringSlice(flagkey.HtMethod)

	for _, method := range methods {
		_, err := GetMethod(method)
		if err != nil {
			return err
		}
	}
	m := metav1.ObjectMeta{
		Name:      triggerName,
		Namespace: userProvidedNS,
	}

	// For Specs, the spec validate checks for function reference
	if input.Bool(flagkey.SpecSave) {
		if err := spec.CheckFunctionReferencesInSpecs(input, "HTTPTrigger", triggerName, functionList, userProvidedNS); err != nil {
			return err
		}
	} else {

		m = metav1.ObjectMeta{
			Name:      triggerName,
			Namespace: fnNamespace,
		}
		htTrigger, err := opts.Client().FissionClientSet.CoreV1().HTTPTriggers(m.Namespace).Get(input.Context(), m.Name, metav1.GetOptions{})

		if err != nil && !kerrors.IsNotFound(err) {
			return err
		}
		if htTrigger != nil && htTrigger.Namespace != "" {
			return errors.New("duplicate trigger exists, choose a different name or leave it empty for fission to auto-generate it")
		}

		err = util.CheckFunctionExistence(input.Context(), opts.Client(), functionList, fnNamespace)
		if err != nil {
			console.Warn(err.Error())
		}
	}

	warnIngressDeprecated(input)

	ingressConfig, err := GetIngressConfig(
		input.StringSlice(flagkey.HtIngressAnnotation), input.String(flagkey.HtIngressRule),
		input.String(flagkey.HtIngressTLS), fallbackURL, nil)
	if err != nil {
		return fmt.Errorf("error parsing ingress configuration: %w", err)
	}

	routeConfig, err := GetRouteConfig(
		input.String(flagkey.HtRouteProvider), input.StringSlice(flagkey.HtRouteHost),
		input.String(flagkey.HtRoutePath), input.StringSlice(flagkey.HtRouteAnnotation),
		input.String(flagkey.HtRouteTLS), input.StringSlice(flagkey.HtGateway), fallbackURL)
	if err != nil {
		return fmt.Errorf("error parsing route configuration: %w", err)
	}

	opts.trigger = &fv1.HTTPTrigger{
		ObjectMeta: m,
		Spec: fv1.HTTPTriggerSpec{
			Host:              input.String(flagkey.HtHost),
			RelativeURL:       triggerUrl,
			Methods:           methods,
			FunctionReference: *functionRef,
			CreateIngress:     input.Bool(flagkey.HtIngress),
			IngressConfig:     *ingressConfig,
			RouteConfig:       routeConfig,
			Prefix:            &prefix,
			KeepPrefix:        input.Bool(flagkey.HtKeepPrefix),
			InvocationMode:    input.String(flagkey.HtInvocationMode),
		},
	}

	return nil
}

func (opts *CreateSubCommand) run(input cli.Input) error {
	// if we're writing a spec, don't call the API; save/print and return.
	if handled, err := spec.SaveOrDry(input, *opts.trigger, fmt.Sprintf("route-%v.yaml", opts.trigger.Name)); handled {
		return err
	}

	// Ensure we don't have a duplicate HTTP route defined (same URL and method)
	err := util.CheckHTTPTriggerDuplicates(input.Context(), opts.Client(), opts.trigger)
	if err != nil {
		return fmt.Errorf("error while creating HTTP Trigger: %w", err)
	}

	_, err = opts.Client().FissionClientSet.CoreV1().HTTPTriggers(opts.trigger.Namespace).Create(input.Context(), opts.trigger, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create HTTP trigger: %w", err)
	}

	fmt.Printf("trigger '%v' created\n", opts.trigger.Name)

	return nil
}

// warnIngressDeprecated emits a deprecation warning when any of the legacy
// --createingress / --ingress* flags are used, pointing users at the Gateway
// API. Shared by the create and update subcommands.
func warnIngressDeprecated(input cli.Input) {
	if input.Bool(flagkey.HtIngress) || input.IsSet(flagkey.HtIngressRule) ||
		input.IsSet(flagkey.HtIngressAnnotation) || input.IsSet(flagkey.HtIngressTLS) {
		console.Warn("--createingress and --ingress* are deprecated: the Kubernetes Ingress API is frozen. " +
			"Expose functions through the Gateway API instead, e.g. --route-provider gateway --gateway <name> --route-host <host>.")
	}
}

// GetMethod returns one of HTTP method
func GetMethod(method string) (string, error) {
	switch strings.ToUpper(method) {
	case http.MethodGet:
		return http.MethodGet, nil
	case http.MethodHead:
		return http.MethodHead, nil
	case http.MethodPost:
		return http.MethodPost, nil
	case http.MethodPut:
		return http.MethodPut, nil
	case http.MethodPatch:
		return http.MethodPatch, nil
	case http.MethodDelete:
		return http.MethodDelete, nil
	case http.MethodConnect:
		return http.MethodConnect, nil
	case http.MethodOptions:
		return http.MethodOptions, nil
	case http.MethodTrace:
		return http.MethodTrace, nil
	default:
		return "", fmt.Errorf("invalid or unsupported HTTP Method '%v'", method)
	}
}

func setHtFunctionRef(functionList []string, functionWeightsList []int) (*fv1.FunctionReference, error) {
	if len(functionList) == 1 {
		return &fv1.FunctionReference{
			Type: fv1.FunctionReferenceTypeFunctionName,
			Name: functionList[0],
		}, nil
	} else if len(functionList) == 2 {
		if len(functionWeightsList) != 2 {
			return nil, fmt.Errorf("weights of the function need to be specified when 2 functions are supplied")
		}

		totalWeight := functionWeightsList[0] + functionWeightsList[1]
		if totalWeight != 100 {
			return nil, errors.New("the function weights should add up to 100")
		}

		functionWeights := make(map[string]int)
		for index := range functionList {
			functionWeights[functionList[index]] = functionWeightsList[index]
		}

		return &fv1.FunctionReference{
			Type:            fv1.FunctionReferenceTypeFunctionWeights,
			FunctionWeights: functionWeights,
		}, nil
	}

	return nil, fmt.Errorf("the number of functions in a trigger can be 1 or 2(for canary feature along with their weights)")
}
