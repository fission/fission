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
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/utils"
)

type ValidateSubCommand struct {
	cmd.CommandActioner
}

// Validate parses a set of specs and checks for references to
// resources that don't exist.
func Validate(input cli.Input) error {
	return (&ValidateSubCommand{}).do(input)
}

func (opts *ValidateSubCommand) do(input cli.Input) error {
	return opts.run(input)
}

func (opts *ValidateSubCommand) run(input cli.Input) error {

	// this will error on parse errors and on duplicates
	specDir := util.GetSpecDir(input)
	fr, err := ReadSpecs(specDir)
	if err != nil {
		return errors.Wrap(err, "error reading specs")
	}

	var warnings []string
	// this does the rest of the checks, like dangling refs
	warnings, err = fr.Validate(input)
	if err != nil {
		return errors.Wrap(err, "error validating specs")
	}
	// check if any of the spec resource is already present in the cluster
	error := ResourceExists(opts, fr)
	if error != nil {
		return errors.Wrap(error, " Spec validation failed")
	}
	fmt.Printf("Spec validation successful\nSpec contains\n %v Functions\n %v Environments\n %v Packages \n %v Http Triggers \n %v MessageQueue Triggers\n %v Time Triggers\n %v Kube Watchers\n %v ArchiveUploadSpec\n",
		len(fr.Functions), len(fr.Environments), len(fr.Packages), len(fr.HttpTriggers), len(fr.MessageQueueTriggers), len(fr.TimeTriggers), len(fr.KubernetesWatchTriggers), len(fr.ArchiveUploadSpecs))

	for _, warning := range warnings {
		console.Warn(warning)
	}
	return nil
}

// ResourceExists checks if the spec resources exists in the cluster
func ResourceExists(opts *ValidateSubCommand, fr *FissionResources) error {
	result := utils.MultiErrorWithFormat()

	fnlist, err := getFunctions(opts)
	if err != nil {
		return errors.Errorf("Unable to get Functions %v", err.Error())
	}
	for _, f := range fnlist {
		for _, fn := range fr.Functions {
			// if the function name is same check if they are present in same namespace
			if f.Metadata.Name == fn.Metadata.Name {
				if f.Metadata.Namespace == fn.Metadata.Namespace {
					result = multierror.Append(result, fmt.Errorf("\nFunction %v/%v already exist", fn.Metadata.Name, fn.Metadata.Namespace))
				}
			}
		}
	}
	envlist, err := getEnvironments(opts)
	if err != nil {
		return errors.Errorf("Unable to get Environments %v", err.Error())
	}
	for _, e := range envlist {
		for _, env := range fr.Environments {
			// if the enviornemt name is same check if they are present in the same namespace
			if e.Metadata.Name == env.Metadata.Name {
				if e.Metadata.Namespace == env.Metadata.Namespace {
					result = multierror.Append(result, fmt.Errorf("\nEnvironment %v/%v already exist", env.Metadata.Name, env.Metadata.Namespace))

				}
			}
		}
	}
	pkglist, err := getPackages(opts)
	if err != nil {
		return errors.Errorf("Unable to get Packages %v", err.Error())
	}
	for _, p := range pkglist {
		for _, pkg := range fr.Packages {
			// if the enviornemt name is same check if they are present in the same namespace
			if p.Metadata.Name == pkg.Metadata.Name {
				if p.Metadata.Namespace == pkg.Metadata.Namespace {
					result = multierror.Append(result, fmt.Errorf("\nPakcage %v/%v already exist", p.Metadata.Name, pkg.Metadata.Namespace))

				}
			}
		}
	}

	httptriggerlist, err := getHTTPTriggers(opts)
	if err != nil {
		return errors.Errorf("Unable to get HTTPTrigger %v", err.Error())
	}
	for _, h := range httptriggerlist {
		for _, htt := range fr.HttpTriggers {
			// if the enviornemt name is same check if they are present in the same namespace
			if h.Metadata.Name == htt.Metadata.Name {
				if h.Metadata.Namespace == htt.Metadata.Namespace {
					result = multierror.Append(result, fmt.Errorf("\n HttpTrigger %v/%v already exist", htt.Metadata.Name, htt.Metadata.Namespace))

				}
			}
		}
	}
	mqtriggerlist, err := getMessageQueueTriggers(opts, "")
	if err != nil {
		return errors.Errorf("Unable to get Message Queue Trigger %v", err.Error())
	}
	for _, m := range mqtriggerlist {
		for _, mqt := range fr.MessageQueueTriggers {
			// if the messagequeue trigger name is same check if they are present in the same namespace
			if m.Metadata.Name == mqt.Metadata.Name {
				if m.Metadata.Namespace == mqt.Metadata.Namespace {
					result = multierror.Append(result, fmt.Errorf("\n MessageQueueTriggers %v/%v already exist", m.Metadata.Name, mqt.Metadata.Namespace))

				}
			}
		}
	}

	timetriggerlist, err := getTimeTriggers(opts)
	if err != nil {
		return errors.Errorf("Unable to get Time Trigger %v", err.Error())
	}
	for _, t := range timetriggerlist {
		for _, tt := range fr.MessageQueueTriggers {
			// if the enviornemt name is same check if they are present in the same namespace
			if t.Metadata.Name == tt.Metadata.Name {
				if t.Metadata.Namespace == tt.Metadata.Namespace {
					result = multierror.Append(result, fmt.Errorf("\n TimeTrigger %v/%v already exist", tt.Metadata.Name, tt.Metadata.Namespace))

				}
			}
		}
	}

	kubewatchtriggerlist, err := getKubeWatchTriggers(opts)
	if err != nil {
		return errors.Errorf("Unable to get Kubernetes Watch Trigger %v", err.Error())
	}
	for _, k := range kubewatchtriggerlist {
		for _, kwt := range fr.KubernetesWatchTriggers {
			// if the enviornemt name is same check if they are present in the same namespace
			if k.Metadata.Name == kwt.Metadata.Name {
				if k.Metadata.Namespace == kwt.Metadata.Namespace {
					result = multierror.Append(result, fmt.Errorf("\n Kubernetes Watch Trigger %v/%v already exist", kwt.Metadata.Name, kwt.Metadata.Namespace))

				}
			}
		}
	}
	return result.ErrorOrNil()
}

// getAllFunctions get lists of functions in all namespaces
func getFunctions(opts *ValidateSubCommand) ([]fv1.Function, error) {
	fns, err := opts.Client().V1().Function().List("")
	if err != nil {
		return nil, errors.Errorf("Unable to get Functions %v", err.Error())
	}
	return fns, nil
}

// getAllEnvironments get lists of environments in all namespaces
func getEnvironments(opts *ValidateSubCommand) ([]fv1.Environment, error) {
	envs, err := opts.Client().V1().Environment().List("")
	if err != nil {
		return nil, errors.Errorf("Unable to get Enviornmets %v", err.Error())
	}
	return envs, nil
}

// getAllPackages get lists of packages in all namespaces
func getPackages(opts *ValidateSubCommand) ([]fv1.Package, error) {
	pkgList, err := opts.Client().V1().Package().List("")
	if err != nil {
		return nil, errors.Errorf("Unable to get Packages %v", err.Error())
	}
	return pkgList, nil
}

// getAllCanaryConfigs get lists of canary configs in all namespaces
func getCanaryConfigs(opts *ValidateSubCommand) ([]fv1.CanaryConfig, error) {
	canaryCfgs, err := opts.Client().V1().CanaryConfig().List("")
	if err != nil {
		return nil, errors.Errorf("Unable to get Canary Configs %v", err.Error())
	}
	return canaryCfgs, nil
}

// getAllHTTPTriggers get lists of  HTTP Triggers in all namespaces
func getHTTPTriggers(opts *ValidateSubCommand) ([]fv1.HTTPTrigger, error) {
	hts, err := opts.Client().V1().HTTPTrigger().List("")
	if err != nil {
		return nil, errors.Errorf("Unable to get HTTP Triggers %v", err.Error())
	}
	return hts, nil
}

// getAllMessageQueueTriggers get lists of  MessageQueue Triggers in all namespaces
func getMessageQueueTriggers(opts *ValidateSubCommand, mqttype string) ([]fv1.MessageQueueTrigger, error) {
	mqts, err := opts.Client().V1().MessageQueueTrigger().List(mqttype, "")
	if err != nil {
		return nil, errors.Errorf("Unable to get MessageQueue Triggers %v", err.Error())
	}
	return mqts, nil
}

// getAllTimeTriggers get lists of  Time Triggers in all namespaces
func getTimeTriggers(opts *ValidateSubCommand) ([]fv1.TimeTrigger, error) {
	tts, err := opts.Client().V1().TimeTrigger().List("")
	if err != nil {
		return nil, errors.Errorf("Unable to get Time Triggers %v", err.Error())
	}
	return tts, nil
}

// getAllKubeWatchTriggers get lists of  Kube Watchers in all namespaces
func getKubeWatchTriggers(opts *ValidateSubCommand) ([]fv1.KubernetesWatchTrigger, error) {
	ws, err := opts.Client().V1().KubeWatcher().List("")
	if err != nil {
		return nil, errors.Errorf("Unable to get Kube Watchers %v", err.Error())
	}
	return ws, nil
}

// ReadSpecs reads all specs in the specified directory and returns a parsed set of
// fission resources.
func ReadSpecs(specDir string) (*FissionResources, error) {

	// make sure spec directory exists before continue
	if _, err := os.Stat(specDir); os.IsNotExist(err) {
		return nil, errors.Errorf("Spec directory %v doesn't exist. "+
			"Please check directory path or run \"fission spec init\" to create it.", specDir)
	}

	fr := FissionResources{
		Packages:                make([]fv1.Package, 0),
		Functions:               make([]fv1.Function, 0),
		Environments:            make([]fv1.Environment, 0),
		HttpTriggers:            make([]fv1.HTTPTrigger, 0),
		KubernetesWatchTriggers: make([]fv1.KubernetesWatchTrigger, 0),
		TimeTriggers:            make([]fv1.TimeTrigger, 0),
		MessageQueueTriggers:    make([]fv1.MessageQueueTrigger, 0),

		SourceMap: SourceMap{
			Locations: make(map[string](map[string](map[string]Location))),
		},
	}

	var result *multierror.Error

	// Users can organize the specdir into subdirs if they want to.
	err := filepath.Walk(specDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// For now just read YAML files. We'll add jsonnet at some point. Skip
		// unsupported files.
		if !(strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
			return nil
		}
		// read
		b, err := ioutil.ReadFile(path)
		if err != nil {
			result = multierror.Append(result, err)
			return nil
		}
		// handle the case where there are multiple YAML docs per file. go-yaml
		// doesn't support this directly, yet.
		docs := bytes.Split(b, []byte("\n---"))
		lines := 1
		for _, doc := range docs {
			d := []byte(strings.TrimSpace(string(doc)))
			if len(d) != 0 {
				// parse this document and add whatever is in it to fr
				err = fr.ParseYaml(d, &Location{
					Path: path,
					Line: lines,
				})
				if err != nil {
					// collect all errors so user can fix them all
					result = multierror.Append(result, err)
				}
			}
			// the separator occupies one line, hence the +1
			lines += strings.Count(string(doc), "\n") + 1
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	if err = result.ErrorOrNil(); err != nil {
		return nil, err
	}

	return &fr, nil
}
