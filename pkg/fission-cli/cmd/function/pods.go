// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"fmt"
	"io"
	"os"

	corev1 "k8s.io/api/core/v1"
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
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in finding pod for function : %w", err)
	}
	// validate function
	_, err = opts.Client().FissionClientSet.CoreV1().Functions(namespace).Get(input.Context(), input.String(flagkey.FnName), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting function: %w", err)
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
		return fmt.Errorf("error listing pods: %w", err)
	}

	printFunctionPodsTo(os.Stdout, activePods(pods.Items))
	return nil
}

// printFunctionPodsTo renders the function-pods table
// (NAME/NAMESPACE/READY/STATUS/IP/EXECUTORTYPE/MANAGED) for the given
// non-terminating pods. Shared by `function pods` and `function describe` so the
// two never drift; READY is ready/total.
func printFunctionPodsTo(out io.Writer, pods []*corev1.Pod) {
	w := util.NewTabWriter(out)
	// SERVED reflects the RFC-0002 data-plane state: a pod carries
	// fission.io/served=true only while it is published to its function's
	// EndpointSlice and actually handling traffic (the idle reaper strips it).
	fmt.Fprintln(w, "NAME\tNAMESPACE\tREADY\tSTATUS\tIP\tEXECUTORTYPE\tMANAGED\tSERVED")
	for _, pod := range pods {
		ready, total := utils.PodContainerReadyStatus(pod)
		fmt.Fprintf(w, "%s\t%s\t%d/%d\t%s\t%s\t%s\t%s\t%s\n",
			pod.Name, pod.Namespace, ready, total, pod.Status.Phase,
			valueOr(pod.Status.PodIP), valueOr(pod.Labels[v1.EXECUTOR_TYPE]), valueOr(pod.Labels[v1.MANAGED]),
			valueOr(pod.Labels[v1.SERVED_LABEL]))
	}
	w.Flush()
}
