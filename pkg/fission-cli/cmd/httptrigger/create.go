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
	"net/http"
	"os"
	"strings"

	"github.com/pkg/errors"
	uuid "github.com/satori/go.uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
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
		triggerName = uuid.NewV4().String()
	}
	fnNamespace := input.String(flagkey.NamespaceFunction)

	m := &metav1.ObjectMeta{
		Name:      triggerName,
		Namespace: fnNamespace,
	}

	htTrigger, err := opts.Client().V1().HTTPTrigger().Get(m)
	if err != nil && !ferror.IsNotFound(err) {
		return err
	}
	if htTrigger != nil {
		return errors.New("duplicate trigger exists, choose a different name or leave it empty for fission to auto-generate it")
	}

	if !input.IsSet(flagkey.HtUrl) || !input.IsSet(flagkey.HtPrefix) {
		console.Error("You need to supply either Prefix or URL/RelativeURL")
		os.Exit(1)
	}

	if input.IsSet(flagkey.HtUrl) && input.IsSet(flagkey.HtPrefix) {
		console.Warn("Prefix will take precedence over URL/RelativeURL")
	}

	triggerUrl := input.String(flagkey.HtUrl)
	if triggerUrl == "/" {
		return errors.New("url with only root path is not allowed")
	} else if !strings.HasPrefix(triggerUrl, "/") {
		triggerUrl = fmt.Sprintf("/%s", triggerUrl)
	}

	prefix := input.String(flagkey.HtPrefix)
	if prefix == "/" {
		return errors.New("url with only root path is not allowed")
	} else if !strings.HasPrefix(prefix, "/") {
		triggerUrl = fmt.Sprintf("/%s", prefix)
	}

	method, err := GetMethod(input.String(flagkey.HtMethod))
	if err != nil {
		return err
	}

	// For Specs, the spec validate checks for function reference
	if input.Bool(flagkey.SpecSave) {
		specDir := util.GetSpecDir(input)
		fr, err := spec.ReadSpecs(specDir)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("error reading spec in '%v'", specDir))
		}
		for _, fn := range functionList {
			exists, err := fr.ExistsInSpecs(fv1.Function{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fn,
					Namespace: fnNamespace,
				},
			})
			if err != nil {
				return err
			}
			if !exists {
				console.Warn(fmt.Sprintf("HTTPTrigger '%v' references unknown Function '%v', please create it before applying spec",
					triggerName, fn))
			}
		}
	} else {
		err = util.CheckFunctionExistence(opts.Client(), functionList, fnNamespace)
		if err != nil {
			console.Warn(err.Error())
		}
	}

	createIngress := input.Bool(flagkey.HtIngress)
	ingressConfig, err := GetIngressConfig(
		input.StringSlice(flagkey.HtIngressAnnotation), input.String(flagkey.HtIngressRule),
		input.String(flagkey.HtIngressTLS), triggerUrl, nil)
	if err != nil {
		return errors.Wrap(err, "error parsing ingress configuration")
	}

	host := input.String(flagkey.HtHost)

	opts.trigger = &fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      triggerName,
			Namespace: fnNamespace,
		},
		Spec: fv1.HTTPTriggerSpec{
			Host:              host,
			RelativeURL:       triggerUrl,
			Method:            method,
			FunctionReference: *functionRef,
			CreateIngress:     createIngress,
			IngressConfig:     *ingressConfig,
			Prefix:            &prefix,
		},
	}

	return nil
}

func (opts *CreateSubCommand) run(input cli.Input) error {
	// if we're writing a spec, don't call the API
	// save to spec file or display the spec to console
	if input.Bool(flagkey.SpecDry) {
		return spec.SpecDry(*opts.trigger)
	}

	if input.Bool(flagkey.SpecSave) {
		specFile := fmt.Sprintf("route-%v.yaml", opts.trigger.ObjectMeta.Name)
		err := spec.SpecSave(*opts.trigger, specFile)
		if err != nil {
			return errors.Wrap(err, "error saving HTTP trigger spec")
		}
		return nil
	}

	_, err := opts.Client().V1().HTTPTrigger().Create(opts.trigger)
	if err != nil {
		return errors.Wrap(err, "create HTTP trigger")
	}

	fmt.Printf("trigger '%v' created\n", opts.trigger.ObjectMeta.Name)

	return nil
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
		return "", fmt.Errorf("invalid or unsupported HTTP Method %v", method)
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
