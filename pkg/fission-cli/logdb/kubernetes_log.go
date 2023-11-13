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

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type LogDBOptions struct {
	Client cmd.Client
}

type kubernetesLogs struct {
	client cmd.Client
}

func (k kubernetesLogs) GetLogs(ctx context.Context, logFilter LogFilter, podLogs *bytes.Buffer) (err error) {
	err = GetFunctionPodLogs(ctx, k.client, logFilter, podLogs)
	return err
}

func NewKubernetesEndpoint(logDBOptions LogDBOptions) (kubernetesLogs, error) {
	return kubernetesLogs{
		client: logDBOptions.Client}, nil
}

// FunctionPodLogs : Get logs for a function directly from pod
func GetFunctionPodLogs(ctx context.Context, client cmd.Client, logFilter LogFilter, podLogs *bytes.Buffer) (err error) {

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

	podNs = util.ResolveFunctionNS(podNs)
	podList, err := client.KubernetesClient.CoreV1().Pods(podNs).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(selector).AsSelector().String(),
	})
	if err != nil {
		return err
	}

	if len(podList.Items) <= 0 {
		return errors.Errorf("no active pods found for function in namespace %s", podNs)
	}

	pods := podList.Items
	if logFilter.AllPods {
		for _, pod := range pods {
			// get the pod with highest resource version
			err = streamContainerLog(ctx, client.KubernetesClient, &pod, logFilter, podLogs)
			if err != nil {
				return errors.Wrapf(err, "error getting container logs")
			}
		}
	} else {
		// Get the logs for last Pod executed
		sort.Slice(pods, func(i, j int) bool {
			rv1, _ := strconv.ParseInt(pods[i].ObjectMeta.ResourceVersion, 10, 32)
			rv2, _ := strconv.ParseInt(pods[j].ObjectMeta.ResourceVersion, 10, 32)
			return rv1 > rv2
		})

		// get the pod with highest resource version
		err = streamContainerLog(ctx, client.KubernetesClient, &pods[0], logFilter, podLogs)
		if err != nil {
			return errors.Wrapf(err, "error getting container logs")
		}
	}

	return err
}

func streamContainerLog(ctx context.Context, kubernetesClient kubernetes.Interface, pod *v1.Pod, logFilter LogFilter, output *bytes.Buffer) (err error) {
	FETCHER := "fetcher"
	for _, container := range pod.Spec.Containers {
		if container.Name == FETCHER {
			continue
		}
		tailLines := int64(logFilter.RecordLimit)
		sinceTime := metav1.NewTime(logFilter.Since)
		podLogOpts := v1.PodLogOptions{Container: container.Name, // Only the env container, not fetcher
			SinceTime: &sinceTime,
			TailLines: &tailLines,
		}

		podLogsReq := kubernetesClient.CoreV1().Pods(pod.Namespace).GetLogs(pod.ObjectMeta.Name, &podLogOpts)

		podLogs, err := podLogsReq.Stream(ctx)
		if err != nil {
			return errors.Wrapf(err, "error streaming pod log")
		}

		if logFilter.Details {
			fn := logFilter.FunctionObject
			msg := fmt.Sprintf("\n=== Function=%s Environment=%s Namespace=%s Pod=%s Container=%s Node=%s\n",
				fn.ObjectMeta.Name, fn.Spec.Environment.Name, pod.Namespace, pod.Name, container.Name, pod.Spec.NodeName)
			if _, err := output.WriteString(msg); err != nil {
				return errors.Wrapf(err, "error copying pod log")
			}
		}

		_, err = io.Copy(output, podLogs)
		if err != nil {
			return errors.Wrapf(err, "error copying pod log")
		}

		podLogs.Close()
	}

	return nil
}
