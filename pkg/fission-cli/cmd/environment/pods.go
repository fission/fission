// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package environment

import (
	"fmt"
	"os"
	"text/tabwriter"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/utils"
)

type ListPodsSubCommand struct {
	cmd.CommandActioner
}

func ListPods(input cli.Input) error {
	return (&ListPodsSubCommand{}).do(input)
}

func (opts *ListPodsSubCommand) do(input cli.Input) (err error) {

	_, currentNS, err := opts.GetResourceNamespace(input, flagkey.NamespaceEnvironment)
	if err != nil {
		return fmt.Errorf("error getting environment pods: %w", err)
	}

	_, err = opts.Client().FissionClientSet.CoreV1().Environments(currentNS).Get(input.Context(), input.String(flagkey.EnvName), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting environment: %w", err)
	}

	// label selector
	selector := map[string]string{
		v1.ENVIRONMENT_NAME: input.String(flagkey.EnvName),
	}
	selector[v1.ENVIRONMENT_NAMESPACE] = currentNS
	if len(input.String(flagkey.EnvExecutorType)) > 0 {
		selector[v1.EXECUTOR_TYPE] = input.String(flagkey.EnvExecutorType)
	}

	podsList, err := opts.Client().KubernetesClient.CoreV1().Pods(metav1.NamespaceAll).List(input.Context(), metav1.ListOptions{
		LabelSelector: labels.Set(selector).AsSelector().String(),
	})
	if err != nil {
		return fmt.Errorf("error listing environments: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\t%v\t%v\t\n", "NAME", "NAMESPACE", "READY", "STATUS", "IP", "EXECUTORTYPE", "MANAGED")
	for _, pod := range podsList.Items {

		// A deletion timestamp indicates that a pod is terminating. Do not count this pod.
		if pod.DeletionTimestamp != nil {
			continue
		}

		labelList := pod.GetLabels()
		readyContainers, noOfContainers := utils.PodContainerReadyStatus(&pod)
		fmt.Fprintf(w, "%v\t%v\t%v/%v\t%v\t%v\t%v\t%v\t\n", pod.Name, pod.Namespace, noOfContainers, readyContainers, pod.Status.Phase, pod.Status.PodIP, labelList[v1.EXECUTOR_TYPE], labelList[v1.MANAGED])
	}
	w.Flush()

	return nil
}
