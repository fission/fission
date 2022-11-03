/*
Copyright 2021 The Fission Authors.

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

package function

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/utils"
)

type ListPodsSubCommand struct {
	cmd.CommandActioner
}

func ListPods(input cli.Input) error {
	return (&ListPodsSubCommand{}).do(input)
}

func (opts *ListPodsSubCommand) do(input cli.Input) error {

	_, namespace, err := util.GetResourceNamespace(input, flagkey.NamespaceFunction)

	if err != nil {
		return errors.Wrap(err, "error in finding pod for function ")
	}
	// validate function
	_, err = opts.Client().FissionClientSet.CoreV1().Functions(namespace).Get(input.Context(), input.String(flagkey.FnName), metav1.GetOptions{})
	if err != nil {
		return errors.Wrap(err, "error getting function")
	}

	selector := map[string]string{
		v1.FUNCTION_NAME: input.String(flagkey.FnName),
	}
	if len(namespace) != 0 {
		selector[v1.FUNCTION_NAMESPACE] = namespace
	}

	pods, err := opts.Client().KubernetesClient.CoreV1().Pods(metav1.NamespaceAll).List(input.Context(), metav1.ListOptions{
		LabelSelector: labels.Set(selector).AsSelector().String(),
	})
	if err != nil {
		return errors.Wrap(err, "error listing environments")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t\n", "NAME", "NAMESPACE", "READY", "STATUS", "IP", "EXECUTORTYPE", "MANAGED")
	for _, pod := range pods.Items {

		// A deletion timestamp indicates that a pod is terminating. Do not count this pod.
		if pod.ObjectMeta.DeletionTimestamp != nil {
			continue
		}

		labelList := pod.GetLabels()
		readyContainers, noOfContainers := utils.PodContainerReadyStatus(&pod)
		fmt.Fprintf(w, "%v\t%v\t%v/%v\t%v\t%v\t%v\t%v\t\n", pod.ObjectMeta.Name, pod.ObjectMeta.Namespace, noOfContainers, readyContainers, pod.Status.Phase, pod.Status.PodIP, labelList[v1.EXECUTOR_TYPE], labelList[v1.MANAGED])
	}
	w.Flush()

	return nil
}
