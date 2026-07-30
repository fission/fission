// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

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
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       "83c82da2-81e9-4ebd-867e-f383e65e603f",
		},
	}
}

// TestGetObjName is a thin delegation smoke test: getObjName reaches
// executorUtils.VersionedObjName with the "newdeploy-" prefix and a
// versioned Function still yields a distinct, bounded name. The full
// length-bound property tests and hash-fallback table live once, against
// VersionedObjName itself, in pkg/executor/util/version_test.go.
func TestGetObjName(t *testing.T) {
	t.Parallel()
	deploy := &NewDeploy{}

	fn := fnForObjName("hello", "default")
	unversioned := deploy.getObjName(fn)
	assert.Equal(t, unversioned, deploy.getObjName(fn), "name must be stable")
	assert.Contains(t, unversioned, "newdeploy-")
	assert.LessOrEqual(t, len(unversioned), 63)

	fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v3"}
	versioned := deploy.getObjName(fn)
	assert.LessOrEqual(t, len(versioned), 63)
	assert.NotEqual(t, unversioned, versioned, "a versioned name must not collide with the unversioned one")
	assert.Contains(t, versioned, "-v3", "the -v<seq> tail must be recognizable in the derived name")
}

// TestGetDeployLabelsPropagatesFunctionVersion asserts FUNCTION_VERSION
// flows from the versioned Function object's labels into the Deployment
// labels via getDeployLabels' existing fnMeta.Labels merge — no new
// merge logic needed, just coverage that the merge actually carries the
// label through.
func TestGetDeployLabelsPropagatesFunctionVersion(t *testing.T) {
	t.Parallel()
	deploy := &NewDeploy{}
	fnMeta := metav1.ObjectMeta{
		Name:      "hello",
		Namespace: "default",
		UID:       "fn-uid",
		Labels:    map[string]string{fv1.FUNCTION_VERSION: "hello-v3"},
	}
	envMeta := metav1.ObjectMeta{Name: "env", Namespace: "default", UID: "env-uid"}

	labels := deploy.getDeployLabels(fnMeta, envMeta)

	assert.Equal(t, "hello-v3", labels[fv1.FUNCTION_VERSION], "FUNCTION_VERSION must propagate to Deployment labels")
}

// TestGetDeployLabelsUnversionedHasNoVersionLabel is the byte-identical
// control: an unversioned function's labels must not carry FUNCTION_VERSION.
func TestGetDeployLabelsUnversionedHasNoVersionLabel(t *testing.T) {
	t.Parallel()
	deploy := &NewDeploy{}
	fnMeta := metav1.ObjectMeta{Name: "hello", Namespace: "default", UID: "fn-uid"}
	envMeta := metav1.ObjectMeta{Name: "env", Namespace: "default", UID: "env-uid"}

	labels := deploy.getDeployLabels(fnMeta, envMeta)

	_, has := labels[fv1.FUNCTION_VERSION]
	assert.False(t, has, "unversioned function must not carry FUNCTION_VERSION label")
}

// TestGetFuncSvcFromCacheVersionPinned is the C1 regression at the manager
// boundary: the executor caches distinct fsvcs for a live function (gen 5)
// and a versioned projection (gen 2) sharing one UID, each backing its own
// Deployment. GetFuncSvcFromCache for the version-pinned projection must
// return the projection's own service — never the live one that a bare-UID
// (last-writer-wins) index would have handed back, silently executing the
// wrong version's code. It then locks the C8 half: evicting the versioned
// entry (IsValid-failure path, DeleteFuncSvcFromCache) must leave the live
// entry reachable through the UID index.
func TestGetFuncSvcFromCacheVersionPinned(t *testing.T) {
	t.Parallel()
	deploy := &NewDeploy{fsCache: fscache.MakeFunctionServiceCache(loggerfactory.GetLogger())}
	ctx := t.Context()

	liveFn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name: "hello", Namespace: "default",
			UID: "83c82da2-81e9-4ebd-867e-f383e65e603f", Generation: 5,
		},
	}
	// Versioned projection: same UID, pinned older Generation, version label
	// (the shape versioning.VersionedFunction produces).
	v1Fn := liveFn.DeepCopy()
	v1Fn.Generation = 2
	v1Fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v1"}

	liveSvc := fscache.FuncSvc{
		Name:     deploy.getObjName(liveFn),
		Function: &liveFn.ObjectMeta,
		Address:  "10.9.0.5:8888",
		Executor: fv1.ExecutorTypeNewdeploy,
	}
	v1Svc := fscache.FuncSvc{
		Name:     deploy.getObjName(v1Fn),
		Function: &v1Fn.ObjectMeta,
		Address:  "10.9.0.2:8888",
		Executor: fv1.ExecutorTypeNewdeploy,
	}
	_, err := deploy.fsCache.Add(liveSvc)
	require.NoError(t, err)
	// Added last: a bare-UID index would leave the versioned meta as the
	// UID's only entry.
	_, err = deploy.fsCache.Add(v1Svc)
	require.NoError(t, err)

	got, err := deploy.GetFuncSvcFromCache(ctx, v1Fn)
	require.NoError(t, err, "version-pinned request must hit its own generation's cache entry")
	assert.Equal(t, v1Svc.Address, got.Address, "gen-2 request must never be routed to the live gen-5 Deployment")
	assert.Equal(t, v1Svc.Name, got.Name)

	got, err = deploy.GetFuncSvcFromCache(ctx, liveFn)
	require.NoError(t, err)
	assert.Equal(t, liveSvc.Address, got.Address, "live request must never be routed to the versioned Deployment")

	// C8: evicting one version's entry must not orphan the survivor's UID
	// index entry (ListByFunctionUID feeds fnDelete; ListOld feeds the idle
	// reaper).
	deploy.DeleteFuncSvcFromCache(ctx, &v1Svc)

	got, err = deploy.GetFuncSvcFromCache(ctx, liveFn)
	require.NoError(t, err, "evicting the versioned entry must leave the live entry reachable")
	assert.Equal(t, liveSvc.Address, got.Address)

	remaining := deploy.fsCache.ListByFunctionUID(liveFn.UID)
	require.Len(t, remaining, 1)
	assert.Equal(t, liveSvc.Address, remaining[0].Address)
}

// seedFunctionObjects creates the Deployment/Service/HPA triple named name
// with the given labels in ns — the object set fnCreate leaves behind.
func seedFunctionObjects(t *testing.T, kubeClient kubernetes.Interface, ns, name string, lbls map[string]string) {
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

// assertFunctionObjects asserts presence (want=true) or absence (want=false)
// of the Deployment/Service/HPA triple named name in ns.
func assertFunctionObjects(t *testing.T, kubeClient kubernetes.Interface, ns, name string, want bool) {
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

// TestNewDeployFnDeleteEmptyCacheRemovesPerVersionObjects is the C4/gap-1
// regression: after an executor restart the fscache is EMPTY, so fnDelete's
// cache walk finds nothing and its computed-name fallback only covers the
// live (unversioned) object set — the per-version -v<seq>
// Deployment/Service/HPA sets specialized by the previous incarnation used
// to leak. Deletion must be cache-independent: everything carrying the
// function's identity labels goes, and an unrelated function's objects
// survive.
func TestNewDeployFnDeleteEmptyCacheRemovesPerVersionObjects(t *testing.T) {
	t.Parallel()
	logger := loggerfactory.GetLogger()
	kubeClient := fake.NewSimpleClientset()
	deploy := &NewDeploy{
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
	envMeta := metav1.ObjectMeta{Name: "env", Namespace: "default", UID: "env-uid"}

	names := []string{deploy.getObjName(liveFn), deploy.getObjName(v1Fn), deploy.getObjName(v2Fn)}
	for i, fn := range []*fv1.Function{liveFn, v1Fn, v2Fn} {
		seedFunctionObjects(t, kubeClient, "default", names[i], deploy.getDeployLabels(fn.ObjectMeta, envMeta))
	}

	// A different function's objects (same labels shape, different UID) must
	// not be caught by the label sweep.
	otherFn := fnForObjName("other", "default")
	otherFn.UID = "00000000-0000-0000-0000-00000000cafe"
	otherName := deploy.getObjName(otherFn)
	seedFunctionObjects(t, kubeClient, "default", otherName, deploy.getDeployLabels(otherFn.ObjectMeta, envMeta))

	// fsCache is intentionally EMPTY: the executor-restart scenario.
	require.NoError(t, deploy.fnDelete(t.Context(), liveFn))

	for _, name := range names {
		assertFunctionObjects(t, kubeClient, "default", name, false)
	}
	assertFunctionObjects(t, kubeClient, "default", otherName, true)
}

// TestNewDeployCleanupFunctionVersion is the C4/gap-2 regression: deleting a
// FunctionVersion CR (retention GC or manual) must remove exactly that
// version's Deployment/Service/HPA set and evict its fscache entry, while the
// live function's objects and cache entry stay untouched.
func TestNewDeployCleanupFunctionVersion(t *testing.T) {
	t.Parallel()
	logger := loggerfactory.GetLogger()
	kubeClient := fake.NewSimpleClientset()
	deploy := &NewDeploy{
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
	envMeta := metav1.ObjectMeta{Name: "env", Namespace: "default", UID: "env-uid"}

	liveName := deploy.getObjName(liveFn)
	v1Name := deploy.getObjName(v1Fn)
	seedFunctionObjects(t, kubeClient, "default", liveName, deploy.getDeployLabels(liveFn.ObjectMeta, envMeta))
	seedFunctionObjects(t, kubeClient, "default", v1Name, deploy.getDeployLabels(v1Fn.ObjectMeta, envMeta))

	_, err := deploy.fsCache.Add(fscache.FuncSvc{
		Name: liveName, Function: &liveFn.ObjectMeta, Address: "10.9.0.5:8888", Executor: fv1.ExecutorTypeNewdeploy,
	})
	require.NoError(t, err)
	_, err = deploy.fsCache.Add(fscache.FuncSvc{
		Name: v1Name, Function: &v1Fn.ObjectMeta, Address: "10.9.0.2:8888", Executor: fv1.ExecutorTypeNewdeploy,
	})
	require.NoError(t, err)

	require.NoError(t, deploy.CleanupFunctionVersion(ctx, "default", "hello-v1"))

	assertFunctionObjects(t, kubeClient, "default", v1Name, false)
	assertFunctionObjects(t, kubeClient, "default", liveName, true)

	assert.Empty(t, deploy.fsCache.ListByFunctionVersion("default", "hello-v1"),
		"the deleted version's cache entry must be evicted")
	remaining := deploy.fsCache.ListByFunctionUID(liveFn.UID)
	require.Len(t, remaining, 1, "the live entry must survive")
	assert.Equal(t, liveName, remaining[0].Name)
}

// TestNewDeployCleanupFunctionVersionCacheOnly covers the half of
// CleanupFunctionVersion the label list cannot see: a cached projection whose
// backing objects are already gone (or never landed) must still be evicted
// via the cache walk, and the call must not error on the objects' absence.
func TestNewDeployCleanupFunctionVersionCacheOnly(t *testing.T) {
	t.Parallel()
	logger := loggerfactory.GetLogger()
	kubeClient := fake.NewSimpleClientset()
	deploy := &NewDeploy{
		logger:           logger,
		kubernetesClient: kubeClient,
		fsCache:          fscache.MakeFunctionServiceCache(logger),
		nsResolver:       utils.DefaultNSResolver(),
		hpaops:           hpautils.NewHpaOperations(logger, kubeClient, "test-instance"),
	}

	v1Fn := fnForObjName("hello", "default")
	v1Fn.Generation = 2
	v1Fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v1"}
	_, err := deploy.fsCache.Add(fscache.FuncSvc{
		Name: deploy.getObjName(v1Fn), Function: &v1Fn.ObjectMeta, Address: "10.9.0.2:8888", Executor: fv1.ExecutorTypeNewdeploy,
	})
	require.NoError(t, err)

	require.NoError(t, deploy.CleanupFunctionVersion(t.Context(), "default", "hello-v1"))
	assert.Empty(t, deploy.fsCache.ListByFunctionVersion("default", "hello-v1"))
}
