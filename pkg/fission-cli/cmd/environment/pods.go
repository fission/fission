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

package environment

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
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
	m := &metav1.ObjectMeta{
		Name:      input.String(flagkey.EnvName),
		Namespace: input.String(flagkey.NamespaceEnvironment),
		Labels: map[string]string{
			fv1.ENVIRONMENT_NAMESPACE: input.String(flagkey.NamespaceEnvironment),
		},
	}

	exType := input.String(flagkey.EnvExecutorType)
	if len(exType) != 0 {
		m.Labels[fv1.EXECUTOR_TYPE] = exType
	}

	gvr, err := util.GetGVRFromAPIVersionKind(util.FISSION_API_VERSION, util.FISSION_ENVIRONMENT)
	util.CheckError(err, "error finding GVR")

	// validate environment
	_, err = opts.Client().DynamicClient().Resource(*gvr).Namespace(m.Namespace).Get(context.TODO(), m.Name, metav1.GetOptions{})
	util.CheckError(err, "error getting environment")

	pods, err := opts.Client().KubeClient().CoreV1().Pods(metav1.NamespaceAll).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labels.Set(m.Labels).AsSelector().String(),
	})
	util.CheckError(err, "error listing pods for environment")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t\n", "NAME", "NAMESPACE", "READY", "STATUS", "IP", "EXECUTORTYPE", "MANAGED")
	for _, pod := range pods.Items {

		// A deletion timestamp indicates that a pod is terminating. Do not count this pod.
		if pod.ObjectMeta.DeletionTimestamp != nil {
			continue
		}

		labelList := pod.GetLabels()
		readyContainers, noOfContainers := utils.PodContainerReadyStatus(&pod)
		fmt.Fprintf(w, "%v\t%v\t%v/%v\t%v\t%v\t%v\t%v\t\n", pod.ObjectMeta.Name, pod.ObjectMeta.Namespace, noOfContainers, readyContainers, pod.Status.Phase, pod.Status.PodIP, labelList[fv1.EXECUTOR_TYPE], labelList[fv1.MANAGED])
	}
	w.Flush()

	return nil
}
