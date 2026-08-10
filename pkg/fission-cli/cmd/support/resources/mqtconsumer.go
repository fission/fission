// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"context"
	"fmt"
	"strings"
	"sync"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fission/fission/pkg/fission-cli/console"
)

// DeploymentIndex holds every Deployment in the cluster, fetched once and
// shared by the dumpers that select on something other than a label: mqt
// consumers, found by owner reference, and orphaned pod events, which attribute
// a pod that no longer exists to the Deployment its name derives from.
type DeploymentIndex struct {
	client  kubernetes.Interface
	once    sync.Once
	items   []appsv1.Deployment
	partial bool
	err     error
}

func NewDeploymentIndex(clientset kubernetes.Interface) *DeploymentIndex {
	return &DeploymentIndex{client: clientset}
}

func (idx *DeploymentIndex) get(ctx context.Context) ([]appsv1.Deployment, bool, error) {
	idx.once.Do(func() {
		idx.items, idx.partial, idx.err = listAllPages("deployment", func(cont string) ([]appsv1.Deployment, string, error) {
			list, err := idx.client.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
				Limit:    listPageSize,
				Continue: cont,
			})
			if err != nil {
				return nil, "", err
			}
			return list.Items, list.Continue, nil
		})
	})
	return idx.items, idx.partial, idx.err
}

// What an MqtConsumerDumper collects.
const (
	MqtConsumerDeployment = "Deployment"
	MqtConsumerPod        = "Pod"
	MqtConsumerLog        = "Log"
)

// MqtConsumerDumper dumps the workload a keda MessageQueueTrigger scales.
//
// The scaler creates a Deployment per mqt (pkg/mqtrigger/scalermanager.go)
// alongside the ScaledObject. Without this the bundle carries the scaling
// policy but neither the spec nor the logs of the thing being scaled, which is
// the first thing wanted for "my keda mqt isn't consuming".
//
// It cannot be a label-selector dumper. The scaler labels that Deployment
// app: <mqt name> and nothing else, so there is no fission-identifying label to
// select on — and adding one is not an option, because Spec.Selector is
// immutable on an existing Deployment and changing the pod template would force
// a rollout of every consumer on upgrade. The MQT owner reference is the signal
// that already exists, and it is the same one KedaDumper uses.
type MqtConsumerDumper struct {
	client      kubernetes.Interface
	deployments *DeploymentIndex
	mode        string
}

func NewMqtConsumerDumper(clientset kubernetes.Interface, deployments *DeploymentIndex, mode string) Resource {
	return MqtConsumerDumper{client: clientset, deployments: deployments, mode: mode}
}

func (res MqtConsumerDumper) Dump(ctx context.Context, dumpDir string) {
	// Validated before anything else: an unknown mode must be reported on every
	// cluster, not only on one that happens to run keda triggers.
	if res.mode != MqtConsumerDeployment && res.mode != MqtConsumerPod && res.mode != MqtConsumerLog {
		console.Error(fmt.Sprintf("Unknown mqt consumer dump mode: %v", res.mode))
		return
	}

	deployments, partial, err := res.deployments.get(ctx)
	if err != nil {
		console.Error(fmt.Sprintf("Error getting deployment list: %v", err))
		writeNote(dumpDir, "_deployment-list-failed.txt", "no mqt consumers dumped: %v", err)
		return
	}
	if partial {
		writeNote(dumpDir, "_partial-deployment-list.txt",
			"the cluster deployment list did not complete; some mqt consumers may be missing here")
	}

	var owned []appsv1.Deployment
	for _, d := range deployments {
		if ownedByMessageQueueTrigger(d.ObjectMeta) {
			owned = append(owned, d)
		}
	}
	if len(owned) == 0 {
		return
	}

	if res.mode == MqtConsumerDeployment {
		for _, d := range owned {
			cleaned := mqtConsumerDeploymentClean(d)
			writeToFile(getFileName(dumpDir, cleaned.ObjectMeta), cleaned)
		}
		return
	}

	for _, d := range owned {
		for _, pod := range res.podsFor(ctx, d) {
			if res.mode == MqtConsumerPod {
				cleaned := mqtConsumerPodClean(pod)
				writeToFile(getFileName(dumpDir, cleaned.ObjectMeta), cleaned)
				continue
			}
			dumpPodContainerLogs(ctx, res.client, pod, dumpDir)
		}
	}
}

// The scaler inlines every mqt.Spec.Metadata value as a literal env var on the
// consumer (pkg/mqtrigger/scalermanager.go), so the map masked on the
// MessageQueueTrigger and on the ScaledObject arrives here a third way — and
// this is the one path where the workload itself, not just its config, is being
// written out. Secret-derived vars use ValueFrom and carry no value, so they
// are already safe and are left alone.
func mqtConsumerDeploymentClean(d appsv1.Deployment) appsv1.Deployment {
	out := d.DeepCopy()
	maskPodSpecEnv(&out.Spec.Template.Spec)
	return *out
}

func mqtConsumerPodClean(p corev1.Pod) corev1.Pod {
	out := p.DeepCopy()
	maskPodSpecEnv(&out.Spec)
	return *out
}

func maskPodSpecEnv(spec *corev1.PodSpec) {
	for i := range spec.Containers {
		maskContainerEnv(&spec.Containers[i])
	}
	for i := range spec.InitContainers {
		maskContainerEnv(&spec.InitContainers[i])
	}
}

func maskContainerEnv(c *corev1.Container) {
	for i := range c.Env {
		env := &c.Env[i]
		if env.Value == "" {
			continue
		}
		if isCredentialKey(env.Name) || hasURLPassword(env.Value) {
			env.Value = "-"
		}
	}
}

// podsFor returns the consumer pods of one mqt Deployment.
//
// The Deployment's own selector is app: <mqt name> — a user-chosen value on the
// most generic label key in kubernetes — so the selector alone would sweep an
// unrelated pod that merely shares that name into a bundle sent to a third
// party. Every pod a Deployment owns is named <deployment>-<rs hash>-<random>,
// so the name prefix is required as well.
func (res MqtConsumerDumper) podsFor(ctx context.Context, d appsv1.Deployment) []corev1.Pod {
	// The typed form is needed because a Deployment carries a *LabelSelector,
	// not the plain map that labels.Set handles elsewhere in this package.
	selector, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	if err != nil {
		console.Error(fmt.Sprintf("Error reading selector of deployment %v/%v: %v", d.Namespace, d.Name, err))
		return nil
	}

	pods, err := res.client.CoreV1().Pods(d.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		console.Error(fmt.Sprintf("Error getting pods of deployment %v/%v: %v", d.Namespace, d.Name, err))
		return nil
	}

	prefix := d.Name + "-"
	out := make([]corev1.Pod, 0, len(pods.Items))
	for _, p := range pods.Items {
		if strings.HasPrefix(p.Name, prefix) {
			out = append(out, p)
		}
	}
	return out
}
