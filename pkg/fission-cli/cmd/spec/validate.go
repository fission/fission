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
	err = ResourceExists(opts, fr)
	if err != nil {
		return errors.Wrap(err, "Spec validation failed")
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

	fnlist, err := getAllFunctions(opts.Client())
	if err != nil {
		return errors.Errorf("Unable to get Functions %v", err.Error())
	}
	for _, f := range fnlist {
		for _, fn := range fr.Functions {
			// if the function name is same check if they are present in same namespace
			if f.ObjectMeta.Name == fn.ObjectMeta.Name &&
				f.ObjectMeta.Namespace == fn.ObjectMeta.Namespace {
				result = multierror.Append(result, fmt.Errorf("%v %v/%v already exists", fn.Kind, fn.ObjectMeta.Name, fn.ObjectMeta.Namespace))
			}
		}
	}
	envlist, err := getAllEnvironments(opts.Client())
	if err != nil {
		return errors.Errorf("Unable to get Environments %v", err.Error())
	}
	for _, e := range envlist {
		for _, env := range fr.Environments {
			// if the enviornemt name is same check if they are present in the same namespace

			if e.ObjectMeta.Name == env.ObjectMeta.Name &&
				e.ObjectMeta.Namespace == env.ObjectMeta.Namespace {
				result = multierror.Append(result, fmt.Errorf("%v %v/%v already exists", env.Kind, env.ObjectMeta.Name, env.ObjectMeta.Namespace))

			}
		}
	}
	pkglist, err := getAllPackages(opts.Client())
	if err != nil {
		return errors.Errorf("Unable to get Packages %v", err.Error())
	}
	for _, p := range pkglist {
		for _, pkg := range fr.Packages {
			// if the package name is same check if they are present in the same namespace
			if p.ObjectMeta.Name == pkg.ObjectMeta.Name &&
				p.ObjectMeta.Namespace == pkg.ObjectMeta.Namespace {
				result = multierror.Append(result, fmt.Errorf("%v %v/%v already exists", p.Kind, p.ObjectMeta.Name, pkg.ObjectMeta.Namespace))
			}
		}
	}

	httptriggerlist, err := getAllHTTPTriggers(opts.Client())
	if err != nil {
		return errors.Errorf("Unable to get HTTPTrigger %v", err.Error())
	}
	for _, h := range httptriggerlist {
		for _, htt := range fr.HttpTriggers {
			// if the HttpTrigger name is same check if they are present in the same namespace
			if h.ObjectMeta.Name == htt.ObjectMeta.Name &&
				h.ObjectMeta.Namespace == htt.ObjectMeta.Namespace {
				result = multierror.Append(result, fmt.Errorf("%v %v/%v already exists", h.Kind, htt.ObjectMeta.Name, htt.ObjectMeta.Namespace))
			}
		}
	}
	mqtriggerlist, err := getAllMessageQueueTriggers(opts.Client(), "")
	if err != nil {
		return errors.Errorf("Unable to get Message Queue Trigger %v", err.Error())
	}
	for _, m := range mqtriggerlist {
		for _, mqt := range fr.MessageQueueTriggers {
			// if the messagequeue trigger name is same check if they are present in the same namespace
			if m.ObjectMeta.Name == mqt.ObjectMeta.Name &&
				m.ObjectMeta.Namespace == mqt.ObjectMeta.Namespace {
				result = multierror.Append(result, fmt.Errorf("%v %v/%v already exists", m.Kind, m.ObjectMeta.Name, mqt.ObjectMeta.Namespace))
			}
		}
	}

	timetriggerlist, err := getAllTimeTriggers(opts.Client())
	if err != nil {
		return errors.Errorf("Unable to get Time Trigger %v", err.Error())
	}
	for _, t := range timetriggerlist {
		for _, tt := range fr.MessageQueueTriggers {
			// if the TimeTrigger name is same check if they are present in the same namespace
			if t.ObjectMeta.Name == tt.ObjectMeta.Name &&
				t.ObjectMeta.Namespace == tt.ObjectMeta.Namespace {
				result = multierror.Append(result, fmt.Errorf("%v %v/%v already exists", tt.Kind, tt.ObjectMeta.Name, tt.ObjectMeta.Namespace))
			}
		}
	}

	kubewatchtriggerlist, err := getAllKubeWatchTriggers(opts.Client())
	if err != nil {
		return errors.Errorf("Unable to get Kubernetes Watch Trigger %v", err.Error())
	}
	for _, k := range kubewatchtriggerlist {
		for _, kwt := range fr.KubernetesWatchTriggers {
			// if the kubewatcher name is same check if they are present in the same namespace
			if k.ObjectMeta.Name == kwt.ObjectMeta.Name &&
				k.ObjectMeta.Namespace == kwt.ObjectMeta.Namespace {
				result = multierror.Append(result, fmt.Errorf("%v %v/%v already exists", kwt.Kind, kwt.ObjectMeta.Name, kwt.ObjectMeta.Namespace))
			}
		}
	}
	return result.ErrorOrNil()
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
