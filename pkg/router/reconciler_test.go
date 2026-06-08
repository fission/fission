// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"testing"
	"time"

	"github.com/bep/debounce"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func newReconcilerTS(t *testing.T, crObjs ...client.Object) (*HTTPTriggerSet, client.Client, *k8sfake.Clientset) {
	t.Helper()
	logger := loggerfactory.GetLogger()
	cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(crObjs...).Build()
	kc := k8sfake.NewClientset()
	ts := &HTTPTriggerSet{
		logger:                     logger,
		kubeClient:                 kc,
		client:                     cl,
		updateRouterRequestChannel: make(chan struct{}, 10),
		syncDebouncer:              debounce.New(time.Millisecond),
		resolver:                   makeFunctionReferenceResolver(logger, cl),
	}
	return ts, cl, kc
}

// requireRebuildSignal asserts the debounced syncTriggers pushed a rebuild
// request onto the channel.
func requireRebuildSignal(t *testing.T, ts *HTTPTriggerSet) {
	t.Helper()
	select {
	case <-ts.updateRouterRequestChannel:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected a mux rebuild signal, got none")
	}
}

func TestHTTPTriggerReconcilerIngressLifecycle(t *testing.T) {
	trigger := &fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "t1", Namespace: "default"},
		Spec: fv1.HTTPTriggerSpec{
			CreateIngress: true,
			RelativeURL:   "/t1",
			Methods:       []string{"GET"},
		},
	}
	ts, cl, kc := newReconcilerTS(t, trigger)
	r := &httpTriggerReconciler{logger: ts.logger, client: cl, ts: ts, providers: []RouteProvider{newIngressRouteProvider(ts.logger, kc)}}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "t1", Namespace: "default"}}

	// Present + CreateIngress -> ingress created, rebuild signalled.
	_, err := r.Reconcile(t.Context(), req)
	require.NoError(t, err)
	_, err = kc.NetworkingV1().Ingresses(podNamespace).Get(t.Context(), "t1", metav1.GetOptions{})
	require.NoError(t, err, "ingress must be created for a CreateIngress trigger")
	requireRebuildSignal(t, ts)

	// Deleted -> ingress removed, rebuild signalled.
	require.NoError(t, cl.Delete(t.Context(), trigger))
	_, err = r.Reconcile(t.Context(), req)
	require.NoError(t, err)
	_, err = kc.NetworkingV1().Ingresses(podNamespace).Get(t.Context(), "t1", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "ingress must be removed when the trigger is deleted")
	requireRebuildSignal(t, ts)
}

func TestHTTPTriggerReconcilerNoIngressStillRebuilds(t *testing.T) {
	trigger := &fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "t2", Namespace: "default"},
		Spec:       fv1.HTTPTriggerSpec{RelativeURL: "/t2", Methods: []string{"GET"}},
	}
	ts, cl, kc := newReconcilerTS(t, trigger)
	r := &httpTriggerReconciler{logger: ts.logger, client: cl, ts: ts, providers: []RouteProvider{newIngressRouteProvider(ts.logger, kc)}}

	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "t2", Namespace: "default"}})
	require.NoError(t, err)
	_, err = kc.NetworkingV1().Ingresses(podNamespace).Get(t.Context(), "t2", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "no ingress should exist for a non-CreateIngress trigger")
	requireRebuildSignal(t, ts)
}

func TestFunctionReconcilerInvalidatesAndRebuilds(t *testing.T) {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn1", Namespace: "default", ResourceVersion: "5"}}
	ts, cl, _ := newReconcilerTS(t, fn)
	r := &functionReconciler{logger: ts.logger, client: cl, ts: ts}

	// Seed a resolver entry that references fn1 at an older ResourceVersion.
	ts.resolver.refCache.Upsert(
		namespacedTriggerReference{namespace: "default", triggerName: "trig", triggerResourceVersion: "1"},
		resolveResult{
			resolveResultType: resolveResultSingleFunction,
			functionMap:       map[string]*fv1.Function{"fn1": {ObjectMeta: metav1.ObjectMeta{Name: "fn1", Namespace: "default", ResourceVersion: "4"}}},
		},
	)

	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "fn1", Namespace: "default"}})
	require.NoError(t, err)

	_, gerr := ts.resolver.refCache.Get(namespacedTriggerReference{namespace: "default", triggerName: "trig", triggerResourceVersion: "1"})
	assert.Error(t, gerr, "stale resolver entry must be invalidated when the function changes")
	requireRebuildSignal(t, ts)
}

func TestFunctionReconcilerDeletedRebuilds(t *testing.T) {
	ts, cl, _ := newReconcilerTS(t)
	r := &functionReconciler{logger: ts.logger, client: cl, ts: ts}
	_, err := r.Reconcile(t.Context(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "gone", Namespace: "default"}})
	require.NoError(t, err)
	requireRebuildSignal(t, ts)
}

func TestResolverResolveByNameViaCache(t *testing.T) {
	fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default"}}
	cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(fn).Build()
	frr := makeFunctionReferenceResolver(loggerfactory.GetLogger(), cl)

	trigger := fv1.HTTPTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "default", ResourceVersion: "1"},
		Spec: fv1.HTTPTriggerSpec{
			FunctionReference: fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "hello"},
		},
	}
	rr, err := frr.resolve(t.Context(), trigger)
	require.NoError(t, err)
	require.NotNil(t, rr.functionMap["hello"])
	assert.Equal(t, "hello", rr.functionMap["hello"].Name)

	// Missing function resolves to an error.
	missing := trigger
	missing.Spec.FunctionReference.Name = "nope"
	missing.ResourceVersion = "2"
	_, err = frr.resolve(t.Context(), missing)
	assert.Error(t, err)
}
