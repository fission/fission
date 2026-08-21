// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versiongc

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/executortype/newdeploy"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	fscheme "github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, fscheme.AddToScheme(s))
	return s
}

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return crfake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
}

// recordingCleaner records CleanupFunctionVersion calls and optionally fails.
type recordingCleaner struct {
	calls [][2]string
	err   error
}

func (r *recordingCleaner) CleanupFunctionVersion(_ context.Context, fnNamespace, versionName string) error {
	r.calls = append(r.calls, [2]string{fnNamespace, versionName})
	return r.err
}

func reconcileReq(ns, name string) ctrl.Request {
	return ctrl.Request{Namespace: ns, Name: name}
}

// TestVersionGCReconcileAliveVersionIsNoop: a CREATE/initial-sync event for a
// still-existing FunctionVersion must tear nothing down.
func TestVersionGCReconcileAliveVersionIsNoop(t *testing.T) {
	t.Parallel()
	v := &fv1.FunctionVersion{Name: "hello-v1", Namespace: "default"}
	cleaner := &recordingCleaner{}
	r := &reconciler{logger: logr.Discard(), client: newFakeClient(t, v), cleaners: []VersionObjectCleaner{cleaner}}

	_, err := r.Reconcile(t.Context(), reconcileReq("default", "hello-v1"))
	require.NoError(t, err)
	assert.Empty(t, cleaner.calls, "an alive FunctionVersion must not trigger teardown")
}

// TestVersionGCReconcileDeletedVersionCallsCleaners: a request for a
// FunctionVersion that is gone (the DELETE-event shape) must invoke every
// cleaner with the version's namespace and name.
func TestVersionGCReconcileDeletedVersionCallsCleaners(t *testing.T) {
	t.Parallel()
	c1, c2 := &recordingCleaner{}, &recordingCleaner{}
	r := &reconciler{logger: logr.Discard(), client: newFakeClient(t), cleaners: []VersionObjectCleaner{c1, c2}}

	_, err := r.Reconcile(t.Context(), reconcileReq("default", "hello-v1"))
	require.NoError(t, err)
	want := [][2]string{{"default", "hello-v1"}}
	assert.Equal(t, want, c1.calls)
	assert.Equal(t, want, c2.calls)
}

// TestVersionGCReconcileCleanerErrorRequeues: a cleaner failure must surface
// as a reconcile error (workqueue backoff-requeue) so a transient API failure
// does not become a permanent leak — and must not stop the other cleaners.
func TestVersionGCReconcileCleanerErrorRequeues(t *testing.T) {
	t.Parallel()
	failing := &recordingCleaner{err: errors.New("api down")}
	ok := &recordingCleaner{}
	r := &reconciler{logger: logr.Discard(), client: newFakeClient(t), cleaners: []VersionObjectCleaner{failing, ok}}

	_, err := r.Reconcile(t.Context(), reconcileReq("default", "hello-v1"))
	require.Error(t, err)
	assert.Len(t, failing.calls, 1)
	assert.Len(t, ok.calls, 1, "one cleaner's failure must not skip the others")
}

// TestVersionGCEndToEndNewdeployTeardown wires a REAL newdeploy manager as
// the cleaner: reconciling a deleted FunctionVersion removes the per-version
// Deployment/Service/HPA set (found via the fv1.FUNCTION_VERSION identity
// label) while the live function's unlabeled set survives — the
// retention-GC-driven teardown, end to end minus the Manager plumbing.
func TestVersionGCEndToEndNewdeployTeardown(t *testing.T) {
	t.Parallel()
	logger := loggerfactory.GetLogger()
	kubeClient := kubefake.NewSimpleClientset()
	fetcherCfg, err := fetcherConfig.MakeFetcherConfig("/userfunc")
	require.NoError(t, err)
	ndm, err := newdeploy.MakeNewDeploy(t.Context(), logger, fissionfake.NewSimpleClientset(), kubeClient, fetcherCfg, "test-instance", nil)
	require.NoError(t, err)
	cleaner, ok := ndm.(VersionObjectCleaner)
	require.True(t, ok, "the newdeploy manager must implement VersionObjectCleaner")

	seed := func(name string, lbls map[string]string) {
		meta := metav1.ObjectMeta{Name: name, Namespace: "default", Labels: lbls}
		_, err := kubeClient.AppsV1().Deployments("default").Create(t.Context(), &appsv1.Deployment{ObjectMeta: meta}, metav1.CreateOptions{})
		require.NoError(t, err)
		_, err = kubeClient.CoreV1().Services("default").Create(t.Context(), &apiv1.Service{ObjectMeta: meta}, metav1.CreateOptions{})
		require.NoError(t, err)
		_, err = kubeClient.AutoscalingV2().HorizontalPodAutoscalers("default").Create(t.Context(), &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: meta}, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	versionLabels := map[string]string{
		fv1.EXECUTOR_TYPE:      string(fv1.ExecutorTypeNewdeploy),
		fv1.FUNCTION_NAME:      "hello",
		fv1.FUNCTION_NAMESPACE: "default",
		fv1.FUNCTION_UID:       "fn-uid",
		fv1.FUNCTION_VERSION:   "hello-v1",
	}
	liveLabels := map[string]string{
		fv1.EXECUTOR_TYPE:      string(fv1.ExecutorTypeNewdeploy),
		fv1.FUNCTION_NAME:      "hello",
		fv1.FUNCTION_NAMESPACE: "default",
		fv1.FUNCTION_UID:       "fn-uid",
	}
	seed("newdeploy-hello-default-abc-v1", versionLabels)
	seed("newdeploy-hello-default-abc", liveLabels)

	r := &reconciler{logger: logr.Discard(), client: newFakeClient(t), cleaners: []VersionObjectCleaner{cleaner}}
	_, err = r.Reconcile(t.Context(), reconcileReq("default", "hello-v1"))
	require.NoError(t, err)

	_, err = kubeClient.AppsV1().Deployments("default").Get(t.Context(), "newdeploy-hello-default-abc-v1", metav1.GetOptions{})
	assert.Error(t, err, "the deleted version's Deployment must be gone")
	_, err = kubeClient.CoreV1().Services("default").Get(t.Context(), "newdeploy-hello-default-abc-v1", metav1.GetOptions{})
	assert.Error(t, err, "the deleted version's Service must be gone")
	_, err = kubeClient.AutoscalingV2().HorizontalPodAutoscalers("default").Get(t.Context(), "newdeploy-hello-default-abc-v1", metav1.GetOptions{})
	assert.Error(t, err, "the deleted version's HPA must be gone")

	_, err = kubeClient.AppsV1().Deployments("default").Get(t.Context(), "newdeploy-hello-default-abc", metav1.GetOptions{})
	assert.NoError(t, err, "the live function's Deployment must survive")
}
