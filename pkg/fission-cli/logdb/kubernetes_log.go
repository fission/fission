/*
Copyright 2016 The Fission Authors.

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

package logdb

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
)

type LogDBOptions struct {
	Client cmd.Client
}

type kubernetesLogs struct {
	ctx    context.Context
	client cmd.Client
}

func (k kubernetesLogs) GetLogs(logFilter LogFilter) (podLogs *bytes.Buffer, err error) {
	podLogs, err = GetFunctionPodLogs(k.ctx, k.client, logFilter)
	return podLogs, err
}

func NewKubernetesEndpoint(ctx context.Context, logDBOptions LogDBOptions) (kubernetesLogs, error) {
	return kubernetesLogs{
		ctx:    ctx,
		client: logDBOptions.Client}, nil
}

// FunctionPodLogs : Get logs for a function directly from pod
func GetFunctionPodLogs(ctx context.Context, client cmd.Client, logFilter LogFilter) (podLogs *bytes.Buffer, err error) {

	f := logFilter.FunctionObject

	podNs := f.Namespace
	if logFilter.PodNamespace != "" {
		podNs = logFilter.PodNamespace
	}
	// Get function Pods first
	selector := map[string]string{
		fv1.FUNCTION_UID:          string(f.ObjectMeta.UID),
		fv1.ENVIRONMENT_NAME:      f.Spec.Environment.Name,
		fv1.ENVIRONMENT_NAMESPACE: f.Spec.Environment.Namespace,
	}
	podList, err := client.KubernetesClient.CoreV1().Pods(podNs).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(selector).AsSelector().String(),
	})
	if err != nil {
		return podLogs, err
	}

	// Get the logs for last Pod executed
	pods := podList.Items
	sort.Slice(pods, func(i, j int) bool {
		rv1, _ := strconv.ParseInt(pods[i].ObjectMeta.ResourceVersion, 10, 32)
		rv2, _ := strconv.ParseInt(pods[j].ObjectMeta.ResourceVersion, 10, 32)
		return rv1 > rv2
	})

	if len(pods) <= 0 {
		console.Warn("version<1.18 used fission-function as pod's default namespace. Specify appropriate namespace with --pod-namespace tag.")
		return podLogs, errors.New("no active pods found")

	}

	// get the pod with highest resource version
	podLogs, err = streamContainerLog(ctx, client.KubernetesClient, &pods[0], logFilter)
	if err != nil {
		return podLogs, errors.Wrapf(err, "error getting container logs")

	}
	return podLogs, err
}

func streamContainerLog(ctx context.Context, kubernetesClient kubernetes.Interface, pod *v1.Pod, logFilter LogFilter) (output *bytes.Buffer, err error) {

	seq := strings.Repeat("=", 35)
	output = new(bytes.Buffer)

	for _, container := range pod.Spec.Containers {
		tailLines := int64(logFilter.RecordLimit)
		sinceTime := metav1.NewTime(logFilter.Since)
		podLogOpts := v1.PodLogOptions{Container: container.Name, // Only the env container, not fetcher
			SinceTime: &sinceTime,
			TailLines: &tailLines,
		}

		podLogsReq := kubernetesClient.CoreV1().Pods(pod.Namespace).GetLogs(pod.ObjectMeta.Name, &podLogOpts)

		podLogs, err := podLogsReq.Stream(ctx)
		if err != nil {
			return output, errors.Wrapf(err, "error streaming pod log")
		}

		if logFilter.Details {
			fn := logFilter.FunctionObject
			msg := fmt.Sprintf("\n%v\nFunction: %v\nEnvironment: %v\nNamespace: %v\nPod: %v\nContainer: %v\nNode: %v\n%v\n", seq,
				fn.ObjectMeta.Name, fn.Spec.Environment.Name, pod.Namespace, pod.Name, container.Name, pod.Spec.NodeName, seq)

			if _, err := output.WriteString(msg); err != nil {
				return output, errors.Wrapf(err, "error copying pod log")
			}
		}

		_, err = io.Copy(output, podLogs)
		if err != nil {
			return output, errors.Wrapf(err, "error copying pod log")
		}

		podLogs.Close()
	}

	return output, nil
}
