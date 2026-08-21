// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/fscache"
	hpautils "github.com/fission/fission/pkg/executor/util/hpa"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// fnForObjName builds a Function with a 36-char UUID-shaped UID (getObjName
// slices the last 17 chars of fn.UID, matching every real Kubernetes UID).
func fnForObjName(name, namespace string) *fv1.Function {
	return &fv1.Function{
		Name:      name,
		Namespace: namespace,
		UID:       "83c82da2-81e9-4ebd-867e-f383e65e603f",
	}
}

// TestContainerGetObjName is a thin delegation smoke test: getObjName
// reaches executorUtils.VersionedObjName with the "container-" prefix and a
// versioned Function still yields a distinct, bounded name. The full
// length-bound property tests and hash-fallback table live once, against
// VersionedObjName itself, in pkg/executor/util/version_test.go.
func TestContainerGetObjName(t *testing.T) {
	t.Parallel()
	caaf := &Container{}

	fn := fnForObjName("hello", "default")
	unversioned := caaf.getObjName(fn)
	assert.Equal(t, unversioned, caaf.getObjName(fn), "name must be stable")
	assert.Contains(t, unversioned, "container-")
	assert.LessOrEqual(t, len(unversioned), 63)

	fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v3"}
	versioned := caaf.getObjName(fn)
	assert.LessOrEqual(t, len(versioned), 63)
	assert.NotEqual(t, unversioned, versioned, "a versioned name must not collide with the unversioned one")
	assert.Contains(t, versioned, "-v3", "the -v<seq> tail must be recognizable in the derived name")
}

// TestContainerGetDeployLabelsPropagatesFunctionVersion asserts
// FUNCTION_VERSION flows from the versioned Function object's labels into
// the Deployment labels via getDeployLabels' existing fnMeta.Labels copy —
// no new merge logic needed, just coverage that the copy actually carries
// the label through.
func TestContainerGetDeployLabelsPropagatesFunctionVersion(t *testing.T) {
	t.Parallel()
	caaf := &Container{}
	fnMeta := metav1.ObjectMeta{
		Name:      "hello",
		Namespace: "default",
		UID:       "fn-uid",
		Labels:    map[string]string{fv1.FUNCTION_VERSION: "hello-v3"},
	}

	labels := caaf.getDeployLabels(fnMeta)

	assert.Equal(t, "hello-v3", labels[fv1.FUNCTION_VERSION], "FUNCTION_VERSION must propagate to Deployment labels")
}

// TestContainerGetDeployLabelsUnversionedHasNoVersionLabel is the
// byte-identical control: an unversioned function's labels must not carry
// FUNCTION_VERSION.
func TestContainerGetDeployLabelsUnversionedHasNoVersionLabel(t *testing.T) {
	t.Parallel()
	caaf := &Container{}
	fnMeta := metav1.ObjectMeta{Name: "hello", Namespace: "default", UID: "fn-uid"}

	labels := caaf.getDeployLabels(fnMeta)

	_, has := labels[fv1.FUNCTION_VERSION]
	assert.False(t, has, "unversioned function must not carry FUNCTION_VERSION label")
}

// TestContainerGetFuncSvcFromCacheVersionPinned is the C1 regression at the
// container-executor boundary — see the newdeploy twin for the full story:
// a version-pinned projection's lookup must return its own generation's
// service (never the live sibling a bare-UID last-writer-wins index handed
// back), and evicting one generation's entry (C8) must leave the survivor
// reachable through the UID index.
func TestContainerGetFuncSvcFromCacheVersionPinned(t *testing.T) {
	t.Parallel()
	caaf := &Container{fsCache: fscache.MakeFunctionServiceCache(loggerfactory.GetLogger())}
	ctx := t.Context()

	liveFn := &fv1.Function{
		Name: "hello", Namespace: "default",
		UID: "83c82da2-81e9-4ebd-867e-f383e65e603f", Generation: 5,
	}
	v1Fn := liveFn.DeepCopy()
	v1Fn.Generation = 2
	v1Fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v1"}

	liveSvc := fscache.FuncSvc{
		Name:     caaf.getObjName(liveFn),
		Function: &liveFn.ObjectMeta,
		Address:  "10.9.1.5:8888",
		Executor: fv1.ExecutorTypeContainer,
	}
	v1Svc := fscache.FuncSvc{
		Name:     caaf.getObjName(v1Fn),
		Function: &v1Fn.ObjectMeta,
		Address:  "10.9.1.2:8888",
		Executor: fv1.ExecutorTypeContainer,
	}
	_, err := caaf.fsCache.Add(liveSvc)
	require.NoError(t, err)
	_, err = caaf.fsCache.Add(v1Svc) // added last: bare-UID index would keep only this meta
	require.NoError(t, err)

	got, err := caaf.GetFuncSvcFromCache(ctx, v1Fn)
	require.NoError(t, err, "version-pinned request must hit its own generation's cache entry")
	assert.Equal(t, v1Svc.Address, got.Address, "gen-2 request must never be routed to the live gen-5 Deployment")

	got, err = caaf.GetFuncSvcFromCache(ctx, liveFn)
	require.NoError(t, err)
	assert.Equal(t, liveSvc.Address, got.Address, "live request must never be routed to the versioned Deployment")

	caaf.DeleteFuncSvcFromCache(ctx, &v1Svc)

	got, err = caaf.GetFuncSvcFromCache(ctx, liveFn)
	require.NoError(t, err, "evicting the versioned entry must leave the live entry reachable")
	assert.Equal(t, liveSvc.Address, got.Address)

	remaining := caaf.fsCache.ListByFunctionUID(liveFn.UID)
	require.Len(t, remaining, 1)
	assert.Equal(t, liveSvc.Address, remaining[0].Address)
}

// seedContainerObjects creates the Deployment/Service/HPA triple named name
// with the given labels in ns — the object set fnCreate leaves behind.
func seedContainerObjects(t *testing.T, kubeClient kubernetes.Interface, ns, name string, lbls map[string]string) {
	t.Helper()
	ctx := t.Context()
	meta := metav1.ObjectMeta{Name: name, Namespace: ns, Labels: lbls}
	_, err := kubeClient.AppsV1().Deployments(ns).Create(ctx, &appsv1.Deployment{ObjectMeta: meta}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClient.CoreV1().Services(ns).Create(ctx, &apiv1.Service{ObjectMeta: meta}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClient.AutoscalingV2().HorizontalPodAutoscalers(ns).Create(ctx, &autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: meta}, metav1.CreateOptions{})
	require.NoError(t, err)
}

// assertContainerObjects asserts presence (want=true) or absence (want=false)
// of the Deployment/Service/HPA triple named name in ns.
func assertContainerObjects(t *testing.T, kubeClient kubernetes.Interface, ns, name string, want bool) {
	t.Helper()
	ctx := t.Context()
	_, depErr := kubeClient.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	_, svcErr := kubeClient.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
	_, hpaErr := kubeClient.AutoscalingV2().HorizontalPodAutoscalers(ns).Get(ctx, name, metav1.GetOptions{})
	if want {
		assert.NoError(t, depErr, "deployment %s must exist", name)
		assert.NoError(t, svcErr, "service %s must exist", name)
		assert.NoError(t, hpaErr, "hpa %s must exist", name)
	} else {
		assert.Error(t, depErr, "deployment %s must be deleted", name)
		assert.Error(t, svcErr, "service %s must be deleted", name)
		assert.Error(t, hpaErr, "hpa %s must be deleted", name)
	}
}

// TestContainerFnDeleteEmptyCacheRemovesPerVersionObjects is the C4/gap-1
// regression at the container-executor boundary — see the newdeploy twin:
// with an EMPTY fscache (executor restart), fnDelete must still remove every
// per-version -v<seq> Deployment/Service/HPA set via the identity-label
// sweep, while an unrelated function's objects survive.
func TestContainerFnDeleteEmptyCacheRemovesPerVersionObjects(t *testing.T) {
	t.Parallel()
	logger := loggerfactory.GetLogger()
	kubeClient := fake.NewSimpleClientset()
	caaf := &Container{
		logger:           logger,
		kubernetesClient: kubeClient,
		fsCache:          fscache.MakeFunctionServiceCache(logger),
		nsResolver:       utils.DefaultNSResolver(),
		hpaops:           hpautils.NewHpaOperations(logger, kubeClient, "test-instance"),
	}

	liveFn := fnForObjName("hello", "default")
	v1Fn := liveFn.DeepCopy()
	v1Fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v1"}
	v2Fn := liveFn.DeepCopy()
	v2Fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v2"}

	names := []string{caaf.getObjName(liveFn), caaf.getObjName(v1Fn), caaf.getObjName(v2Fn)}
	for i, fn := range []*fv1.Function{liveFn, v1Fn, v2Fn} {
		seedContainerObjects(t, kubeClient, "default", names[i], caaf.getDeployLabels(fn.ObjectMeta))
	}

	otherFn := fnForObjName("other", "default")
	otherFn.UID = "00000000-0000-0000-0000-00000000cafe"
	otherName := caaf.getObjName(otherFn)
	seedContainerObjects(t, kubeClient, "default", otherName, caaf.getDeployLabels(otherFn.ObjectMeta))

	// fsCache is intentionally EMPTY: the executor-restart scenario.
	require.NoError(t, caaf.fnDelete(t.Context(), liveFn))

	for _, name := range names {
		assertContainerObjects(t, kubeClient, "default", name, false)
	}
	assertContainerObjects(t, kubeClient, "default", otherName, true)
}

// TestContainerCleanupFunctionVersion is the C4/gap-2 regression at the
// container-executor boundary — see the newdeploy twin: deleting a
// FunctionVersion CR must remove exactly that version's object set and evict
// its fscache entry, leaving the live function's objects and entry untouched.
func TestContainerCleanupFunctionVersion(t *testing.T) {
	t.Parallel()
	logger := loggerfactory.GetLogger()
	kubeClient := fake.NewSimpleClientset()
	caaf := &Container{
		logger:           logger,
		kubernetesClient: kubeClient,
		fsCache:          fscache.MakeFunctionServiceCache(logger),
		nsResolver:       utils.DefaultNSResolver(),
		hpaops:           hpautils.NewHpaOperations(logger, kubeClient, "test-instance"),
	}
	ctx := t.Context()

	liveFn := fnForObjName("hello", "default")
	liveFn.Generation = 5
	v1Fn := liveFn.DeepCopy()
	v1Fn.Generation = 2
	v1Fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v1"}

	liveName := caaf.getObjName(liveFn)
	v1Name := caaf.getObjName(v1Fn)
	seedContainerObjects(t, kubeClient, "default", liveName, caaf.getDeployLabels(liveFn.ObjectMeta))
	seedContainerObjects(t, kubeClient, "default", v1Name, caaf.getDeployLabels(v1Fn.ObjectMeta))

	_, err := caaf.fsCache.Add(fscache.FuncSvc{
		Name: liveName, Function: &liveFn.ObjectMeta, Address: "10.9.1.5:8888", Executor: fv1.ExecutorTypeContainer,
	})
	require.NoError(t, err)
	_, err = caaf.fsCache.Add(fscache.FuncSvc{
		Name: v1Name, Function: &v1Fn.ObjectMeta, Address: "10.9.1.2:8888", Executor: fv1.ExecutorTypeContainer,
	})
	require.NoError(t, err)

	require.NoError(t, caaf.CleanupFunctionVersion(ctx, "default", "hello-v1"))

	assertContainerObjects(t, kubeClient, "default", v1Name, false)
	assertContainerObjects(t, kubeClient, "default", liveName, true)

	assert.Empty(t, caaf.fsCache.ListByFunctionVersion("default", "hello-v1"),
		"the deleted version's cache entry must be evicted")
	remaining := caaf.fsCache.ListByFunctionUID(liveFn.UID)
	require.Len(t, remaining, 1, "the live entry must survive")
	assert.Equal(t, liveName, remaining[0].Name)
}
