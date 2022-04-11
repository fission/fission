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

package environment

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type DeleteSubCommand struct {
	cmd.CommandActioner
}

func Delete(input cli.Input) error {
	return (&DeleteSubCommand{}).do(input)
}

func (opts *DeleteSubCommand) do(input cli.Input) error {
	m := &metav1.ObjectMeta{
		Name:      input.String(flagkey.EnvName),
		Namespace: input.String(flagkey.NamespaceEnvironment),
	}

	if !input.Bool(flagkey.EnvForce) {
		fnGvr, err := util.GetGVRFromAPIVersionKind(util.FISSION_API_VERSION, util.FISSION_FUNCTION)
		util.CheckError(err, "error finding GVR")

		resp, err := opts.Client().DynamicClient().Resource(*fnGvr).Namespace(metav1.NamespaceAll).List(context.TODO(), metav1.ListOptions{})
		util.CheckError(err, "error getting functions wrt environment")

		var fnList *fv1.FunctionList
		err = runtime.DefaultUnstructuredConverter.FromUnstructured(resp.UnstructuredContent(), &fnList)
		util.CheckError(err, "error converting unstructured object to FunctionList")

		for _, fn := range fnList.Items {
			if fn.Spec.Environment.Name == m.Name &&
				fn.Spec.Environment.Namespace == m.Namespace {
				return errors.New("Environment is used by atleast one function.")
			}
		}
	}

	gvr, err := util.GetGVRFromAPIVersionKind(util.FISSION_API_VERSION, util.FISSION_ENVIRONMENT)
	util.CheckError(err, "error finding GVR")

	err = opts.Client().DynamicClient().Resource(*gvr).Namespace(m.Namespace).Delete(context.TODO(), m.Name, metav1.DeleteOptions{})
	if err != nil {
		if input.Bool(flagkey.IgnoreNotFound) && util.IsNotFound(err) {
			return nil
		}
		return errors.Wrap(err, "error deleting environment")
	}

	fmt.Printf("environment '%v' deleted\n", m.Name)

	return nil
}
