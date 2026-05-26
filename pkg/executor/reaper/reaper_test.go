// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package reaper

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apiv1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

const reaperNS = metav1.NamespaceDefault

func metaWithAnnotation(name, id string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: reaperNS, Annotations: map[string]string{fv1.EXECUTOR_INSTANCEID_LABEL: id}}
}

func metaWithLabel(name, id string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: reaperNS, Labels: map[string]string{fv1.EXECUTOR_INSTANCEID_LABEL: id}}
}

func metaPlain(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: reaperNS}
}

func TestGetReaperNamespace(t *testing.T) {
	ns := GetReaperNamespace()
	assert.NotEmpty(t, ns)
	assert.Contains(t, ns, reaperNS)
}

func TestCleanupDeployments(t *testing.T) {
	t.Parallel()
	client := fake.NewClientset(
		&appsv1.Deployment{ObjectMeta: metaWithAnnotation("stale", "old")},
		&appsv1.Deployment{ObjectMeta: metaWithLabel("stale-label", "old")},
		&appsv1.Deployment{ObjectMeta: metaWithAnnotation("current", "cur")},
		&appsv1.Deployment{ObjectMeta: metaPlain("nolabel")},
	)

	require.NoError(t, CleanupDeployments(t.Context(), logr.Discard(), client, "cur", metav1.ListOptions{}))

	assertDeleted(t, t.Context(), func(ctx context.Context, name string) error {
		_, err := client.AppsV1().Deployments(reaperNS).Get(ctx, name, metav1.GetOptions{})
		return err
	})
}

func TestCleanupPods(t *testing.T) {
	t.Parallel()
	client := fake.NewClientset(
		&apiv1.Pod{ObjectMeta: metaWithAnnotation("stale", "old")},
		&apiv1.Pod{ObjectMeta: metaWithLabel("stale-label", "old")},
		&apiv1.Pod{ObjectMeta: metaWithAnnotation("current", "cur")},
		&apiv1.Pod{ObjectMeta: metaPlain("nolabel")},
	)

	require.NoError(t, CleanupPods(t.Context(), logr.Discard(), client, "cur", metav1.ListOptions{}))

	assertDeleted(t, t.Context(), func(ctx context.Context, name string) error {
		_, err := client.CoreV1().Pods(reaperNS).Get(ctx, name, metav1.GetOptions{})
		return err
	})
}

func TestCleanupServices(t *testing.T) {
	t.Parallel()
	client := fake.NewClientset(
		&apiv1.Service{ObjectMeta: metaWithAnnotation("stale", "old")},
		&apiv1.Service{ObjectMeta: metaWithLabel("stale-label", "old")},
		&apiv1.Service{ObjectMeta: metaWithAnnotation("current", "cur")},
		&apiv1.Service{ObjectMeta: metaPlain("nolabel")},
	)

	require.NoError(t, CleanupServices(t.Context(), logr.Discard(), client, "cur", metav1.ListOptions{}))

	assertDeleted(t, t.Context(), func(ctx context.Context, name string) error {
		_, err := client.CoreV1().Services(reaperNS).Get(ctx, name, metav1.GetOptions{})
		return err
	})
}

func TestCleanupHpa(t *testing.T) {
	t.Parallel()
	client := fake.NewClientset(
		&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metaWithAnnotation("stale", "old")},
		&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metaWithLabel("stale-label", "old")},
		&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metaWithAnnotation("current", "cur")},
		&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metaPlain("nolabel")},
	)

	require.NoError(t, CleanupHpa(t.Context(), logr.Discard(), client, "cur", metav1.ListOptions{}))

	assertDeleted(t, t.Context(), func(ctx context.Context, name string) error {
		_, err := client.AutoscalingV2().HorizontalPodAutoscalers(reaperNS).Get(ctx, name, metav1.GetOptions{})
		return err
	})
}

// assertDeleted checks the standard fixture outcome: objects belonging to a
// different instance id (via annotation or legacy label) are gone, while the
// current instance's object and unlabelled objects survive.
func assertDeleted(t *testing.T, ctx context.Context, get func(context.Context, string) error) {
	t.Helper()
	for _, gone := range []string{"stale", "stale-label"} {
		err := get(ctx, gone)
		assert.True(t, k8serrors.IsNotFound(err), "%s should have been cleaned up", gone)
	}
	for _, kept := range []string{"current", "nolabel"} {
		assert.NoError(t, get(ctx, kept), "%s should be retained", kept)
	}
}

func TestCleanupKubeObject(t *testing.T) {
	t.Parallel()
	newClient := func() kubernetes.Interface {
		return fake.NewClientset(
			&apiv1.Pod{ObjectMeta: metaPlain("p")},
			&apiv1.Service{ObjectMeta: metaPlain("s")},
			&appsv1.Deployment{ObjectMeta: metaPlain("d")},
			&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: metaPlain("h")},
		)
	}

	tests := []struct {
		kind string
		name string
		get  func(context.Context, kubernetes.Interface, string) error
	}{
		{"Pod", "p", func(ctx context.Context, c kubernetes.Interface, n string) error {
			_, err := c.CoreV1().Pods(reaperNS).Get(ctx, n, metav1.GetOptions{})
			return err
		}},
		{"Service", "s", func(ctx context.Context, c kubernetes.Interface, n string) error {
			_, err := c.CoreV1().Services(reaperNS).Get(ctx, n, metav1.GetOptions{})
			return err
		}},
		{"Deployment", "d", func(ctx context.Context, c kubernetes.Interface, n string) error {
			_, err := c.AppsV1().Deployments(reaperNS).Get(ctx, n, metav1.GetOptions{})
			return err
		}},
		{"HorizontalPodAutoscaler", "h", func(ctx context.Context, c kubernetes.Interface, n string) error {
			_, err := c.AutoscalingV2().HorizontalPodAutoscalers(reaperNS).Get(ctx, n, metav1.GetOptions{})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			t.Parallel()
			client := newClient()
			ref := &apiv1.ObjectReference{Kind: tt.kind, Namespace: reaperNS, Name: tt.name}
			CleanupKubeObject(t.Context(), logr.Discard(), client, ref)
			err := tt.get(t.Context(), client, tt.name)
			assert.True(t, k8serrors.IsNotFound(err), "%s should be deleted", tt.kind)
		})
	}

	t.Run("unknown kind is a no-op", func(t *testing.T) {
		t.Parallel()
		client := newClient()
		ref := &apiv1.ObjectReference{Kind: "ConfigMap", Namespace: reaperNS, Name: "x"}
		CleanupKubeObject(t.Context(), logr.Discard(), client, ref) // must not panic
	})

	t.Run("missing object is tolerated", func(t *testing.T) {
		t.Parallel()
		client := fake.NewClientset()
		ref := &apiv1.ObjectReference{Kind: "Pod", Namespace: reaperNS, Name: "ghost"}
		CleanupKubeObject(t.Context(), logr.Discard(), client, ref) // NotFound is ignored
	})
}
