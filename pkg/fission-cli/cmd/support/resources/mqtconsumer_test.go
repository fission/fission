// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func mqtDeployment(name, namespace string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace, ResourceVersion: "1",
			Labels:          map[string]string{"app": name},
			OwnerReferences: []metav1.OwnerReference{mqtOwnerRef()},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
		},
	}
}

func appPod(name, namespace, app string, uid types.UID) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace, UID: uid, ResourceVersion: "1",
			Labels: map[string]string{"app": app},
		},
	}
}

func TestMqtConsumerDumperDumpsOnlyOwnedDeployments(t *testing.T) {
	t.Parallel()

	ours := mqtDeployment("my-mqt", "default")
	theirs := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "someone-elses", Namespace: "default", ResourceVersion: "1",
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "rs"}},
		},
	}

	client := k8sfake.NewClientset(ours, theirs)
	dir := t.TempDir()
	NewMqtConsumerDumper(client, NewDeploymentIndex(client), MqtConsumerDeployment).Dump(t.Context(), dir)

	files := dumpedFiles(t, dir)
	require.Len(t, files, 1, "only MessageQueueTrigger-owned deployments are fission's")
	assert.Contains(t, files[0], "my-mqt")
}

func TestMqtConsumerDumperIgnoresUnrelatedPodsSharingTheAppLabel(t *testing.T) {
	t.Parallel()

	// app=<mqt name> is the scaler's selector: a user-chosen value on the most
	// generic label key there is. An unrelated pod that happens to carry the
	// same app label must not have its spec written into a bundle that goes to
	// a third party. Every pod the Deployment owns is named <deployment>-...
	deploy := mqtDeployment("my-mqt", "default")
	consumer := appPod("my-mqt-7d9f8b6c5-abcde", "default", "my-mqt", "uid-consumer")
	impostor := appPod("someone-elses-app", "default", "my-mqt", "uid-impostor")

	client := k8sfake.NewClientset(deploy, consumer, impostor)
	dir := t.TempDir()
	NewMqtConsumerDumper(client, NewDeploymentIndex(client), MqtConsumerPod).Dump(t.Context(), dir)

	files := dumpedFiles(t, dir)
	require.Len(t, files, 1)
	assert.Contains(t, files[0], "my-mqt-7d9f8b6c5-abcde")
}

func TestMqtConsumerDumperWritesNothingWithoutKedaTriggers(t *testing.T) {
	t.Parallel()

	client := k8sfake.NewClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "router", Namespace: "fission", ResourceVersion: "1"},
	})

	dir := t.TempDir()
	NewMqtConsumerDumper(client, NewDeploymentIndex(client), MqtConsumerDeployment).Dump(t.Context(), dir)

	assert.Empty(t, dumpedFiles(t, dir), "an install with no keda mqts should add no files")
}

func TestDeploymentIndexListsOnceAcrossDumpers(t *testing.T) {
	t.Parallel()

	client := k8sfake.NewClientset(mqtDeployment("my-mqt", "default"))

	var lists int
	client.PrependReactor("list", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		lists++
		return false, nil, nil
	})

	idx := NewDeploymentIndex(client)
	for _, mode := range []string{MqtConsumerDeployment, MqtConsumerPod, MqtConsumerLog} {
		NewMqtConsumerDumper(client, idx, mode).Dump(t.Context(), t.TempDir())
	}

	assert.Equal(t, 1, lists, "the deployment list is shared across the three consumer dumpers")
}

func TestMqtConsumerDumperMasksInlinedMetadataCredentials(t *testing.T) {
	t.Parallel()

	// The scaler inlines every mqt.Spec.Metadata value as a literal env var on
	// the consumer, so masking the MessageQueueTrigger and the ScaledObject is
	// not enough — the workload itself carries the same values.
	deploy := mqtDeployment("my-mqt", "default")
	deploy.Spec.Template.Spec.Containers = []corev1.Container{{
		Name: "consumer",
		Env: []corev1.EnvVar{
			{Name: "PASSWORD", Value: "hunter2"},
			{Name: "HOST", Value: "amqp://user:hunter2@rabbit:5672/"},
			{Name: "TOPIC", Value: "orders"},
			{Name: "PASSWORD_FROM_ENV", Value: "RABBIT_PASSWORD"},
			{Name: "SECRET_REF", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{Key: "password"},
			}},
		},
	}}

	client := k8sfake.NewClientset(deploy)
	dir := t.TempDir()
	NewMqtConsumerDumper(client, NewDeploymentIndex(client), MqtConsumerDeployment).Dump(t.Context(), dir)

	files := dumpedFiles(t, dir)
	require.Len(t, files, 1)
	content, err := os.ReadFile(filepath.Join(dir, files[0]))
	require.NoError(t, err)
	body := string(content)

	assert.NotContains(t, body, "hunter2", "an inlined credential must not reach a support bundle")
	assert.Contains(t, body, "orders", "non-credential env stays diagnostic")
	assert.Contains(t, body, "RABBIT_PASSWORD",
		"a *FromEnv value names an env var and is the first thing to check on a broken trigger")
	assert.Contains(t, body, "SECRET_REF", "secret-backed env carries no value and is left alone")
}

func TestMqtConsumerDumperMasksInlinedCredentialsOnPods(t *testing.T) {
	t.Parallel()

	deploy := mqtDeployment("my-mqt", "default")
	pod := appPod("my-mqt-7d9f8b6c5-abcde", "default", "my-mqt", "uid-consumer")
	pod.Spec.Containers = []corev1.Container{{
		Name: "consumer",
		Env:  []corev1.EnvVar{{Name: "PASSWORD", Value: "hunter2"}},
	}}

	client := k8sfake.NewClientset(deploy, pod)
	dir := t.TempDir()
	NewMqtConsumerDumper(client, NewDeploymentIndex(client), MqtConsumerPod).Dump(t.Context(), dir)

	files := dumpedFiles(t, dir)
	require.Len(t, files, 1)
	content, err := os.ReadFile(filepath.Join(dir, files[0]))
	require.NoError(t, err)

	assert.NotContains(t, string(content), "hunter2",
		"the pod carries the resolved env, so it leaks the same values as the deployment")
}

func TestMqtConsumerDumperDoesNotMutateTheListedDeployment(t *testing.T) {
	t.Parallel()

	deploy := mqtDeployment("my-mqt", "default")
	deploy.Spec.Template.Spec.Containers = []corev1.Container{{
		Name: "consumer",
		Env:  []corev1.EnvVar{{Name: "PASSWORD", Value: "hunter2"}},
	}}

	cleaned := mqtConsumerDeploymentClean(*deploy)

	assert.Equal(t, "-", cleaned.Spec.Template.Spec.Containers[0].Env[0].Value)
	assert.Equal(t, "hunter2", deploy.Spec.Template.Spec.Containers[0].Env[0].Value,
		"the deployment is shared with the index and must not be edited in place")
}

func TestMqtConsumerDumperRejectsAnUnknownModeOnAnyCluster(t *testing.T) {
	t.Parallel()

	// The guard must not sit behind the "no keda triggers" early return, or a
	// bad mode is silently accepted on most clusters.
	client := k8sfake.NewClientset()
	dir := t.TempDir()

	require.NotPanics(t, func() {
		NewMqtConsumerDumper(client, NewDeploymentIndex(client), "NotAMode").Dump(t.Context(), dir)
	})
	assert.Empty(t, dumpedFiles(t, dir))
}

func TestMqtConsumerDumperOnlyLooksInTheDeploymentsNamespace(t *testing.T) {
	t.Parallel()

	deploy := mqtDeployment("my-mqt", "default")
	elsewhere := appPod("my-mqt-7d9f8b6c5-abcde", "other", "my-mqt", "uid-elsewhere")

	client := k8sfake.NewClientset(deploy, elsewhere)
	dir := t.TempDir()
	NewMqtConsumerDumper(client, NewDeploymentIndex(client), MqtConsumerPod).Dump(t.Context(), dir)

	assert.Empty(t, dumpedFiles(t, dir),
		"a same-named pod in another namespace belongs to a different workload")
}

func TestMqtConsumerDumperReportsADeploymentListFailure(t *testing.T) {
	t.Parallel()

	client := k8sfake.NewClientset()
	client.PrependReactor("list", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "deployments"}, "", assert.AnError)
	})

	idx := NewDeploymentIndex(client)
	for _, mode := range []string{MqtConsumerDeployment, MqtConsumerPod, MqtConsumerLog} {
		dir := t.TempDir()
		require.NotPanics(t, func() {
			NewMqtConsumerDumper(client, idx, mode).Dump(t.Context(), dir)
		})
		assert.Equal(t, []string{"_deployment-list-failed.txt"}, dumpedFiles(t, dir),
			"an empty directory alone reads as an install with no keda triggers")
	}
}

func TestDeploymentIndexPagesThroughEveryDeployment(t *testing.T) {
	t.Parallel()

	client := k8sfake.NewClientset()
	client.PrependReactor("list", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
		if a.(clienttesting.ListActionImpl).GetListOptions().Continue == "" {
			return true, &appsv1.DeploymentList{ListMeta: metav1.ListMeta{Continue: "page-2"}}, nil
		}
		return true, &appsv1.DeploymentList{
			Items: []appsv1.Deployment{*mqtDeployment("my-mqt", "default")},
		}, nil
	})

	dir := t.TempDir()
	NewMqtConsumerDumper(client, NewDeploymentIndex(client), MqtConsumerDeployment).Dump(t.Context(), dir)

	assert.Len(t, dumpedFiles(t, dir), 1, "a consumer on the second page must still be collected")
}

func TestMqtConsumerDumperWritesALogFilePerContainer(t *testing.T) {
	t.Parallel()

	deploy := mqtDeployment("my-mqt", "default")
	pod := appPod("my-mqt-7d9f8b6c5-abcde", "default", "my-mqt", "uid-consumer")
	pod.Spec.Containers = []corev1.Container{{Name: "consumer"}}
	pod.Spec.InitContainers = []corev1.Container{{Name: "fetcher"}}

	client := k8sfake.NewClientset(deploy, pod)
	dir := t.TempDir()
	NewMqtConsumerDumper(client, NewDeploymentIndex(client), MqtConsumerLog).Dump(t.Context(), dir)

	files := dumpedFiles(t, dir)
	require.Len(t, files, 2, "init containers are collected too — they explain a pod that never started")
	assert.Contains(t, strings.Join(files, " "), "consumer")
	assert.Contains(t, strings.Join(files, " "), "fetcher")
}
