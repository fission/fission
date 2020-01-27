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
	"github.com/fission/fission/pkg/controller/client"
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

	console.Infof("DeployUID: %v", fr.DeploymentConfig.UID)
	console.Infof("Resources:\n * %v Functions\n * %v Environments\n * %v Packages \n * %v Http Triggers \n * %v MessageQueue Triggers\n * %v Time Triggers\n * %v Kube Watchers\n * %v ArchiveUploadSpec\n",
		len(fr.Functions), len(fr.Environments), len(fr.Packages), len(fr.HttpTriggers), len(fr.MessageQueueTriggers), len(fr.TimeTriggers), len(fr.KubernetesWatchTriggers), len(fr.ArchiveUploadSpecs))

	var warnings []string
	// this does the rest of the checks, like dangling refs
	warnings, err = fr.Validate(input)
	if err != nil {
		return errors.Wrap(err, "error validating specs")
	}

	err = resourceConflictCheck(opts.Client(), fr)
	if err != nil {
		return errors.Wrap(err, "name conflict error")
	}

	for _, warning := range warnings {
		console.Warn(warning)
	}

	console.Info("Validation Successful")

	return nil
}

// resourceConflictCheck checks if any of the spec resources with
// the same name is already present in the same cluster namespace.
// If a same name resource exists in the same namespace, a name
// conflict error will be returned.
func resourceConflictCheck(c client.Interface, fr *FissionResources) error {
	deployUID := fr.DeploymentConfig.UID
	result := utils.MultiErrorWithFormat()

	fnList, err := getAllFunctions(c)
	if err != nil {
		return errors.Errorf("Unable to get Functions %v", err.Error())
	}
	for _, sObj := range fr.Functions {
		for _, cObj := range fnList {
			if err := isResourceConflicts(deployUID, &sObj, &cObj); err != nil {
				result = multierror.Append(result, err)
				break
			}
		}
	}

	envList, err := getAllEnvironments(c)
	if err != nil {
		return errors.Errorf("Unable to get Environments %v", err.Error())
	}
	for _, sObj := range fr.Environments {
		for _, cObj := range envList {
			if err := isResourceConflicts(deployUID, &sObj, &cObj); err != nil {
				result = multierror.Append(result, err)
				break
			}
		}
	}

	pkgList, err := getAllPackages(c)
	if err != nil {
		return errors.Errorf("Unable to get Packages %v", err.Error())
	}
	for _, sObj := range fr.Packages {
		for _, cObj := range pkgList {
			if err := isResourceConflicts(deployUID, &sObj, &cObj); err != nil {
				result = multierror.Append(result, err)
				break
			}
		}
	}

	httptriggerList, err := getAllHTTPTriggers(c)
	if err != nil {
		return errors.Errorf("Unable to get HTTPTrigger %v", err.Error())
	}
	for _, sObj := range fr.HttpTriggers {
		for _, cObj := range httptriggerList {
			if err := isResourceConflicts(deployUID, &sObj, &cObj); err != nil {
				result = multierror.Append(result, err)
				break
			}
		}
	}

	mqtriggerList, err := getAllMessageQueueTriggers(c, "")
	if err != nil {
		return errors.Errorf("Unable to get Message Queue Trigger %v", err.Error())
	}
	for _, sObj := range fr.MessageQueueTriggers {
		for _, cObj := range mqtriggerList {
			if err := isResourceConflicts(deployUID, &sObj, &cObj); err != nil {
				result = multierror.Append(result, err)
				break
			}
		}
	}

	timetriggerList, err := getAllTimeTriggers(c)
	if err != nil {
		return errors.Errorf("Unable to get Time Trigger %v", err.Error())
	}
	for _, sObj := range fr.TimeTriggers {
		for _, cObj := range timetriggerList {
			if err := isResourceConflicts(deployUID, &sObj, &cObj); err != nil {
				result = multierror.Append(result, err)
				break
			}
		}
	}

	kubewatchtriggerList, err := getAllKubeWatchTriggers(c)
	if err != nil {
		return errors.Errorf("Unable to get Kubernetes Watch Trigger %v", err.Error())
	}
	for _, sObj := range fr.KubernetesWatchTriggers {
		for _, cObj := range kubewatchtriggerList {
			if err := isResourceConflicts(deployUID, &sObj, &cObj); err != nil {
				result = multierror.Append(result, err)
				break
			}
		}
	}

	return result.ErrorOrNil()
}

func isResourceConflicts(deployUID string, specObj fv1.MetadataAccessor, clusterObj fv1.MetadataAccessor) error {
	if specObj.GetObjectMeta().GetName() == clusterObj.GetObjectMeta().GetName() &&
		specObj.GetObjectMeta().GetNamespace() == clusterObj.GetObjectMeta().GetNamespace() &&
		deployUID != clusterObj.GetObjectMeta().GetAnnotations()[FISSION_DEPLOYMENT_UID_KEY] {
		return fmt.Errorf("%v: '%v/%v' with different deploy uid already exists",
			clusterObj.GetObjectKind().GroupVersionKind().Kind, clusterObj.GetObjectMeta().GetName(), clusterObj.GetObjectMeta().GetNamespace())
	}
	return nil
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
