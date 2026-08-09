// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
