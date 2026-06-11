// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"strconv"
	"sync"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

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
}

func TestSpecializedPodLabels(t *testing.T) {
	t.Parallel()
	gp := &GenericPool{
		env: &fv1.Environment{ObjectMeta: metav1.ObjectMeta{Name: "env", Namespace: "default", UID: "env-uid"}},
	}
	meta := &metav1.ObjectMeta{Name: "fn", Namespace: "default", UID: "uid-1", Generation: 7}

	withGen := gp.specializedPodLabels(meta)
	assert.Equal(t, strconv.FormatInt(7, 10), withGen[fv1.FUNCTION_GENERATION])

	// The list/selection path must stay generation-agnostic so RefreshFuncPods
	// still matches pods of every generation.
	listLabels := gp.labelsForFunction(meta)
	_, hasGen := listLabels[fv1.FUNCTION_GENERATION]
	assert.False(t, hasGen)
}
