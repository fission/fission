// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package logdb

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"sync"

	"golang.org/x/sync/errgroup"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/util"
)

const fetcherContainer = "fetcher"

type LogDBOptions struct {
	Client cmd.Client
}

func init() {
	// The zero-dependency default: reads pod logs directly from the Kubernetes API.
	Register(KUBERNETES, func(_ context.Context, opts LogDBOptions) (LogDatabase, error) {
		return NewKubernetesEndpoint(opts)
	})
}

// kubernetesLogs implements both one-shot pod-log reads (LogDatabase) and live
// tailing (LogStreamer).
var _ LogStreamer = kubernetesLogs{}

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
	pods, err := selectFunctionPods(ctx, client, logFilter)
	if err != nil {
		return err
	}
	for i := range pods {
		if err = streamContainerLog(ctx, client.KubernetesClient, &pods[i], logFilter, podLogs); err != nil {
			return fmt.Errorf("error getting container logs: %w", err)
		}
	}
	return nil
}

// selectFunctionPods resolves the pods whose logs `function logs` should read:
// all of them with --all-pods, otherwise just the most recently created one
// (highest resourceVersion). Shared by the one-shot and the --follow paths.
func selectFunctionPods(ctx context.Context, client cmd.Client, logFilter LogFilter) ([]v1.Pod, error) {
	f := logFilter.FunctionObject

	podNs := f.Namespace
	if logFilter.PodNamespace != "" {
		podNs = logFilter.PodNamespace
	}
	var selector map[string]string
	if f.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != fv1.ExecutorTypeContainer {
		selector = map[string]string{
			fv1.FUNCTION_UID:          string(f.UID),
			fv1.ENVIRONMENT_NAME:      f.Spec.Environment.Name,
			fv1.ENVIRONMENT_NAMESPACE: f.Spec.Environment.Namespace,
		}
	} else {
		selector = map[string]string{
			fv1.FUNCTION_UID: string(f.UID),
		}
	}

	podNs = util.ResolveFunctionNS(podNs)
	podList, err := client.KubernetesClient.CoreV1().Pods(podNs).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(selector).AsSelector().String(),
	})
	if err != nil {
		return nil, err
	}
	if len(podList.Items) == 0 {
		return nil, fmt.Errorf("no active pods found for function in namespace %s", podNs)
	}

	pods := podList.Items
	if logFilter.AllPods {
		return pods, nil
	}
	sort.Slice(pods, func(i, j int) bool {
		rv1, _ := strconv.ParseInt(pods[i].ResourceVersion, 10, 32)
		rv2, _ := strconv.ParseInt(pods[j].ResourceVersion, 10, 32)
		return rv1 > rv2
	})
	return pods[:1], nil
}

// StreamLogs follows the function's pod logs live (PodLogOptions.Follow), one
// goroutine per env container, until the context is cancelled. Writes are
// serialized and (when following more than one pod) prefixed with the pod name.
func (k kubernetesLogs) StreamLogs(ctx context.Context, filter LogFilter, out io.Writer) error {
	pods, err := selectFunctionPods(ctx, k.client, filter)
	if err != nil {
		return err
	}
	lw := &lockedWriter{w: out}
	multi := len(pods) > 1
	g, gctx := errgroup.WithContext(ctx)
	for i := range pods {
		pod := pods[i]
		for _, c := range pod.Spec.Containers {
			if c.Name == fetcherContainer {
				continue
			}
			ns, name, container := pod.Namespace, pod.Name, c.Name
			prefix := ""
			if multi {
				prefix = name + " "
			}
			g.Go(func() error {
				return followContainer(gctx, k.client.KubernetesClient, ns, name, container, prefix, filter, lw)
			})
		}
	}
	err = g.Wait()
	// A cancelled context (the user stopped --follow) is a clean exit.
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func followContainer(ctx context.Context, kc kubernetes.Interface, ns, podName, container, prefix string, filter LogFilter, out io.Writer) error {
	opts := &v1.PodLogOptions{Container: container, Follow: true}
	if filter.RecordLimit > 0 {
		tail := int64(filter.RecordLimit)
		opts.TailLines = &tail
	}
	stream, err := kc.CoreV1().Pods(ns).GetLogs(podName, opts).Stream(ctx)
	if err != nil {
		return fmt.Errorf("error streaming pod log: %w", err)
	}
	defer stream.Close()

	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // tolerate long log lines
	for sc.Scan() {
		if _, err := fmt.Fprintf(out, "%s%s\n", prefix, sc.Text()); err != nil {
			return err
		}
	}
	return sc.Err()
}

// lockedWriter serializes concurrent writes from multiple pod-log streams onto
// one writer, so whole lines from different pods don't interleave mid-line.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
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

		podLogsReq := kubernetesClient.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &podLogOpts)

		podLogs, err := podLogsReq.Stream(ctx)
		if err != nil {
			return fmt.Errorf("error streaming pod log: %w", err)
		}

		if logFilter.Details {
			fn := logFilter.FunctionObject
			msg := fmt.Sprintf("\n=== Function=%s Environment=%s Namespace=%s Pod=%s Container=%s Node=%s\n",
				fn.Name, fn.Spec.Environment.Name, pod.Namespace, pod.Name, container.Name, pod.Spec.NodeName)
			if _, err := output.WriteString(msg); err != nil {
				return fmt.Errorf("error copying pod log: %w", err)
			}
		}

		_, err = io.Copy(output, podLogs)
		if err != nil {
			return fmt.Errorf("error copying pod log: %w", err)
		}

		podLogs.Close()
	}

	return nil
}
