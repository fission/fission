// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"fmt"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"pgregory.net/rapid"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
)

func fnForService(name string, generation int64) *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "default",
			UID:        "uid-1234",
			Generation: generation,
		},
	}
}

func gpmForServiceTest(t *testing.T, objects ...runtime.Object) (*GenericPoolManager, *fake.Clientset) {
	t.Helper()
	kubeClient := fake.NewSimpleClientset(objects...)
	return &GenericPoolManager{
		logger:           logr.Discard(),
		kubernetesClient: kubeClient,
		nsResolver:       utils.DefaultNSResolver(),
		instanceID:       "inst-1",
	}, kubeClient
}

func TestFunctionServiceName(t *testing.T) {
	t.Parallel()

	t.Run("deterministic and prefixed", func(t *testing.T) {
		t.Parallel()
		fn := fnForService("hello", 1)
		name := functionServiceName(fn)
		assert.Equal(t, name, functionServiceName(fn), "name must be stable")
		assert.Contains(t, name, "fn-hello-")
		assert.LessOrEqual(t, len(name), 63)
	})

	t.Run("long function names are truncated to the service name limit", func(t *testing.T) {
		t.Parallel()
		long := make([]byte, 100)
		for i := range long {
			long[i] = 'a'
		}
		fn := fnForService(string(long), 1)
		name := functionServiceName(fn)
		assert.LessOrEqual(t, len(name), 63)
		assert.Contains(t, name, "fn-aaaa")
	})

	t.Run("same name different uid yields different services", func(t *testing.T) {
		t.Parallel()
		fnA := fnForService("hello", 1)
		fnB := fnForService("hello", 1)
		fnB.UID = "uid-5678"
		assert.NotEqual(t, functionServiceName(fnA), functionServiceName(fnB))
	})

	t.Run("versioned function gets a distinct, suffixed name", func(t *testing.T) {
		t.Parallel()
		fn := fnForService("hello", 1)
		unversioned := functionServiceName(fn)

		fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v3"}
		versioned := functionServiceName(fn)

		assert.LessOrEqual(t, len(versioned), 63)
		assert.NotEqual(t, unversioned, versioned, "a versioned Service must not collide with the unversioned one")
		assert.Contains(t, versioned, "-v3", "the -v<seq> tail must be recognizable in the derived name")
		assert.Equal(t, versioned, functionServiceName(fn), "name must be stable")
	})

	t.Run("two versions of the same function get distinct names", func(t *testing.T) {
		t.Parallel()
		fnV1 := fnForService("hello", 1)
		fnV1.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v1"}
		fnV2 := fnForService("hello", 1)
		fnV2.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v2"}

		assert.NotEqual(t, functionServiceName(fnV1), functionServiceName(fnV2))
	})
}

// TestFunctionServiceNameLengthBound is the RFC-0025 bound test (written
// before the versioned-suffix implementation, per TDD): for ANY function
// name length, UID, and published-version sequence number, both the
// unversioned and versioned derived Service names must fit the Kubernetes
// 63-char Service name limit, and a versioned name must never collide with
// its function's unversioned name.
func TestFunctionServiceNameLengthBound(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		name := rapid.StringMatching(`[a-z]([a-z0-9-]{0,251}[a-z0-9])?`).Draw(rt, "name")
		uid := rapid.StringMatching(`[a-f0-9-]{1,64}`).Draw(rt, "uid")
		seq := rapid.Int64Range(1, math.MaxInt64).Draw(rt, "seq")

		fn := &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				UID:  types.UID(uid),
			},
		}
		unversioned := functionServiceName(fn)
		require.LessOrEqual(rt, len(unversioned), 63, "unversioned name must fit the 63-char limit")

		fn.Labels = map[string]string{
			fv1.FUNCTION_VERSION: fmt.Sprintf("%s-v%d", name, seq),
		}
		versioned := functionServiceName(fn)
		require.LessOrEqual(rt, len(versioned), 63, "versioned name must fit the 63-char limit")
		require.NotEqual(rt, unversioned, versioned, "a versioned name must never collide with the unversioned name")
		require.Equal(rt, versioned, functionServiceName(fn), "name must be deterministic")
	})
}

func TestEnsureFunctionService(t *testing.T) {
	t.Parallel()

	t.Run("creates a headless service with generation-scoped served selector", func(t *testing.T) {
		t.Parallel()
		gpm, kubeClient := gpmForServiceTest(t)
		fn := fnForService("hello", 3)

		require.NoError(t, gpm.ensureFunctionService(t.Context(), fn))

		svc, err := kubeClient.CoreV1().Services("default").Get(t.Context(), functionServiceName(fn), metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, apiv1.ClusterIPNone, svc.Spec.ClusterIP, "service must be headless")
		assert.Equal(t, map[string]string{
			fv1.FUNCTION_UID:        "uid-1234",
			fv1.FUNCTION_GENERATION: "3",
			fv1.SERVED_LABEL:        "true",
		}, svc.Spec.Selector)
		assert.Equal(t, fv1.MANAGED_BY_VALUE, svc.Labels[fv1.MANAGED_BY_LABEL])
		assert.Equal(t, string(fv1.ExecutorTypePoolmgr), svc.Labels[fv1.EXECUTOR_TYPE])
		assert.Equal(t, "hello", svc.Labels[fv1.FUNCTION_NAME])
		assert.Equal(t, "default", svc.Labels[fv1.FUNCTION_NAMESPACE])
		assert.Equal(t, "inst-1", svc.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL])
	})

	t.Run("versioned function stamps FUNCTION_VERSION on labels and selector", func(t *testing.T) {
		t.Parallel()
		gpm, kubeClient := gpmForServiceTest(t)
		fn := fnForService("hello", 3)
		fn.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v2"}

		require.NoError(t, gpm.ensureFunctionService(t.Context(), fn))

		svc, err := kubeClient.CoreV1().Services("default").Get(t.Context(), functionServiceName(fn), metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "hello-v2", svc.Labels[fv1.FUNCTION_VERSION],
			"the version label must be mirrored onto the Service -- this is what the EndpointSlice controller mirrors onto slices")
		assert.Equal(t, "hello-v2", svc.Spec.Selector[fv1.FUNCTION_VERSION])
		// The unversioned Service for the same function must be a distinct object.
		unversioned := fnForService("hello", 3)
		_, err = kubeClient.CoreV1().Services("default").Get(t.Context(), functionServiceName(unversioned), metav1.GetOptions{})
		assert.True(t, kerrors.IsNotFound(err), "the unversioned Service must not have been created as a side effect")
	})

	t.Run("re-ensure of an unchanged service performs no write", func(t *testing.T) {
		t.Parallel()
		gpm, kubeClient := gpmForServiceTest(t)
		fn := fnForService("hello", 3)
		require.NoError(t, gpm.ensureFunctionService(t.Context(), fn))

		var writes []string
		kubeClient.PrependReactor("update", "services", func(action k8stesting.Action) (bool, runtime.Object, error) {
			writes = append(writes, "update")
			return false, nil, nil
		})
		kubeClient.PrependReactor("create", "services", func(action k8stesting.Action) (bool, runtime.Object, error) {
			writes = append(writes, "create")
			return false, nil, nil
		})

		require.NoError(t, gpm.ensureFunctionService(t.Context(), fn))
		assert.Empty(t, writes, "steady-state ensure must be read-only")
	})

	t.Run("generation drift updates the selector", func(t *testing.T) {
		t.Parallel()
		gpm, kubeClient := gpmForServiceTest(t)
		fn := fnForService("hello", 3)
		require.NoError(t, gpm.ensureFunctionService(t.Context(), fn))

		fn.Generation = 4
		require.NoError(t, gpm.ensureFunctionService(t.Context(), fn))

		svc, err := kubeClient.CoreV1().Services("default").Get(t.Context(), functionServiceName(fn), metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "4", svc.Spec.Selector[fv1.FUNCTION_GENERATION],
			"selector must track the function generation so stale pods drop out of slices")
	})

	t.Run("instanceID drift is re-stamped", func(t *testing.T) {
		t.Parallel()
		gpm, kubeClient := gpmForServiceTest(t)
		fn := fnForService("hello", 3)
		require.NoError(t, gpm.ensureFunctionService(t.Context(), fn))

		gpm.instanceID = "inst-2"
		require.NoError(t, gpm.ensureFunctionService(t.Context(), fn))

		svc, err := kubeClient.CoreV1().Services("default").Get(t.Context(), functionServiceName(fn), metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "inst-2", svc.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL])
	})

	t.Run("concurrent ensures tolerate the create race", func(t *testing.T) {
		t.Parallel()
		gpm, _ := gpmForServiceTest(t)
		fn := fnForService("hello", 3)

		var wg sync.WaitGroup
		errs := make([]error, 8)
		for i := range errs {
			wg.Go(func() {
				errs[i] = gpm.ensureFunctionService(t.Context(), fn)
			})
		}
		wg.Wait()
		for i := range errs {
			assert.NoError(t, errs[i], "ensure %d", i)
		}
	})
}

func TestDeleteFunctionService(t *testing.T) {
	t.Parallel()

	t.Run("deletes the service", func(t *testing.T) {
		t.Parallel()
		gpm, kubeClient := gpmForServiceTest(t)
		fn := fnForService("hello", 1)
		require.NoError(t, gpm.ensureFunctionService(t.Context(), fn))

		require.NoError(t, gpm.deleteFunctionService(t.Context(), fn))
		_, err := kubeClient.CoreV1().Services("default").Get(t.Context(), functionServiceName(fn), metav1.GetOptions{})
		assert.Error(t, err, "service must be gone")
	})

	t.Run("missing service is success", func(t *testing.T) {
		t.Parallel()
		gpm, _ := gpmForServiceTest(t)
		assert.NoError(t, gpm.deleteFunctionService(t.Context(), fnForService("never-created", 1)))
	})

	// BLOCKER FIX 2: deleteFunctionService used to delete only the single
	// unversioned functionServiceName(fn), orphaning every per-version
	// Service on function delete. It must now List-and-delete every Service
	// this function owns.
	t.Run("deletes every per-version service along with the unversioned one", func(t *testing.T) {
		t.Parallel()
		gpm, kubeClient := gpmForServiceTest(t)

		fn := fnForService("hello", 3)
		require.NoError(t, gpm.ensureFunctionService(t.Context(), fn))

		fnV1 := fnForService("hello", 3)
		fnV1.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v1"}
		require.NoError(t, gpm.ensureFunctionService(t.Context(), fnV1))

		fnV2 := fnForService("hello", 3)
		fnV2.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v2"}
		require.NoError(t, gpm.ensureFunctionService(t.Context(), fnV2))

		// Sanity: three distinct Services actually exist before delete.
		svcs, err := kubeClient.CoreV1().Services("default").List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err)
		require.Len(t, svcs.Items, 3)

		require.NoError(t, gpm.deleteFunctionService(t.Context(), fn))

		for _, f := range []*fv1.Function{fn, fnV1, fnV2} {
			_, err := kubeClient.CoreV1().Services("default").Get(t.Context(), functionServiceName(f), metav1.GetOptions{})
			assert.Truef(t, kerrors.IsNotFound(err), "service for labels %v must be gone, got err=%v", f.Labels, err)
		}
	})

	t.Run("does not delete a different function's service", func(t *testing.T) {
		t.Parallel()
		gpm, kubeClient := gpmForServiceTest(t)

		fnA := fnForService("hello", 1)
		require.NoError(t, gpm.ensureFunctionService(t.Context(), fnA))
		fnB := fnForService("hello", 1)
		fnB.UID = "uid-other"
		require.NoError(t, gpm.ensureFunctionService(t.Context(), fnB))

		require.NoError(t, gpm.deleteFunctionService(t.Context(), fnA))

		_, err := kubeClient.CoreV1().Services("default").Get(t.Context(), functionServiceName(fnB), metav1.GetOptions{})
		assert.NoError(t, err, "a different function's service must survive")
	})
}

// TestFnSvcEnsureKey pins the BLOCKER FIX 1 debounce key shape: (UID,
// version) so two versions of one function never share a debounce window.
func TestFnSvcEnsureKey(t *testing.T) {
	t.Parallel()

	unversioned := fnForService("hello", 1)
	v1 := fnForService("hello", 1)
	v1.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v1"}
	v2 := fnForService("hello", 1)
	v2.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v2"}

	assert.Equal(t, "uid-1234/", fnSvcEnsureKey(unversioned), "unversioned key stays UID-equivalent")
	assert.NotEqual(t, fnSvcEnsureKey(unversioned), fnSvcEnsureKey(v1))
	assert.NotEqual(t, fnSvcEnsureKey(v1), fnSvcEnsureKey(v2), "different versions of the same function get different keys")
	assert.Equal(t, fnSvcEnsureKey(v1), fnSvcEnsureKey(v1), "the key is deterministic")
}

// TestMaybeEnsureFunctionServiceDebouncePerVersion is the BLOCKER FIX 1
// regression: a debounced v1 ensure must never starve v2's ensure.
func TestMaybeEnsureFunctionServiceDebouncePerVersion(t *testing.T) {
	t.Parallel()
	gpm, kubeClient := gpmForServiceTest(t)
	gpm.functionServicesEnabled = true

	fnV1 := fnForService("hello", 3)
	fnV1.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v1"}
	fnV2 := fnForService("hello", 3)
	fnV2.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v2"}

	// v1's ensure is already inside its 30s debounce window.
	gpm.fnSvcEnsured.Store(fnSvcEnsureKey(fnV1), time.Now())

	gpm.maybeEnsureFunctionService(fnV2)

	require.Eventually(t, func() bool {
		_, err := kubeClient.CoreV1().Services("default").Get(t.Context(), functionServiceName(fnV2), metav1.GetOptions{})
		return err == nil
	}, 2*time.Second, 10*time.Millisecond, "v2's Service must be ensured despite v1's debounce window")

	// v1 itself must still be debounced (not re-ensured) inside its window.
	_, err := kubeClient.CoreV1().Services("default").Get(t.Context(), functionServiceName(fnV1), metav1.GetOptions{})
	assert.True(t, kerrors.IsNotFound(err), "v1 must remain debounced inside its window")
}

// TestDeleteFnSvcEnsuredForUID pins the companion fix to BLOCKER FIX 1:
// markFuncDeleted only knows a UID, but the debounce map now keys on (UID,
// version), so the sweep must find and remove every version's entry for
// that UID and leave other UIDs' entries alone.
func TestDeleteFnSvcEnsuredForUID(t *testing.T) {
	t.Parallel()
	gpm, _ := gpmForServiceTest(t)

	fnV1 := fnForService("hello", 1)
	fnV1.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v1"}
	fnV2 := fnForService("hello", 1)
	fnV2.Labels = map[string]string{fv1.FUNCTION_VERSION: "hello-v2"}
	other := fnForService("other", 1)
	other.UID = "uid-other"

	gpm.fnSvcEnsured.Store(fnSvcEnsureKey(fnV1), time.Now())
	gpm.fnSvcEnsured.Store(fnSvcEnsureKey(fnV2), time.Now())
	gpm.fnSvcEnsured.Store(fnSvcEnsureKey(other), time.Now())

	gpm.deleteFnSvcEnsuredForUID(fnV1.UID)

	_, ok := gpm.fnSvcEnsured.Load(fnSvcEnsureKey(fnV1))
	assert.False(t, ok, "v1's entry must be removed")
	_, ok = gpm.fnSvcEnsured.Load(fnSvcEnsureKey(fnV2))
	assert.False(t, ok, "v2's entry (same UID) must also be removed")
	_, ok = gpm.fnSvcEnsured.Load(fnSvcEnsureKey(other))
	assert.True(t, ok, "a different UID's entry must survive")
}

func TestSpecializedPodLabels(t *testing.T) {
	t.Parallel()
	gp := &GenericPool{
		env: &fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "env", Namespace: "default", UID: "env-uid"}},
	}
	meta := &metav1.ObjectMeta{Name: "fn", Namespace: "default", UID: "uid-1", Generation: 7}

	withGen := gp.specializedPodLabels(meta)
	assert.Equal(t, strconv.FormatInt(7, 10), withGen[fv1.FUNCTION_GENERATION])
	_, hasVersion := withGen[fv1.FUNCTION_VERSION]
	assert.False(t, hasVersion, "no version label on metadata means none on the pod")

	// The list/selection path must stay generation-agnostic so RefreshFuncPods
	// still matches pods of every generation.
	listLabels := gp.labelsForFunction(meta)
	_, hasGen := listLabels[fv1.FUNCTION_GENERATION]
	assert.False(t, hasGen)

	versionedMeta := &metav1.ObjectMeta{
		Name: "fn", Namespace: "default", UID: "uid-1", Generation: 7,
		Labels: map[string]string{fv1.FUNCTION_VERSION: "fn-v2"},
	}
	versioned := gp.specializedPodLabels(versionedMeta)
	assert.Equal(t, "fn-v2", versioned[fv1.FUNCTION_VERSION], "version label from metadata must carry onto the specialized pod")
}
