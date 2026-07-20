// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/executor/fscache"
	hpautils "github.com/fission/fission/pkg/executor/util/hpa"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func newTestContainer() *Container {
	return &Container{
		logger:                 loggerfactory.GetLogger(),
		kubernetesClient:       fake.NewClientset(),
		runtimeImagePullPolicy: apiv1.PullIfNotPresent,
	}
}

func newTestContainerFunction() *fv1.Function {
	grace := int64(30)
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "ctr-fn", Namespace: "default"},
		Spec: fv1.FunctionSpec{
			InvokeStrategy: fv1.InvokeStrategy{
				ExecutionStrategy: fv1.ExecutionStrategy{ExecutorType: fv1.ExecutorTypeContainer, MinScale: 1, MaxScale: 3},
			},
			PodSpec: &apiv1.PodSpec{
				TerminationGracePeriodSeconds: &grace,
				Containers: []apiv1.Container{
					{Name: "ctr-fn", Image: "example/app:latest"},
				},
			},
		},
	}
}

func findContainer(pod apiv1.PodSpec, name string) *apiv1.Container {
	for i := range pod.Containers {
		if pod.Containers[i].Name == name {
			return &pod.Containers[i]
		}
	}
	return nil
}

func TestContainerGetDeploymentSpec(t *testing.T) {
	t.Run("builds a deployment from the function pod spec", func(t *testing.T) {
		cn := newTestContainer()
		fn := newTestContainerFunction()
		replicas := int32(1)

		deployment, err := cn.getDeploymentSpec(t.Context(), fn, &replicas, "ctr-fn", "default",
			map[string]string{"app": "ctr"}, map[string]string{"note": "x"})
		require.NoError(t, err)

		assert.Equal(t, "ctr-fn", deployment.Name)
		assert.Equal(t, map[string]string{"app": "ctr"}, deployment.Labels)
		assert.Equal(t, map[string]string{"note": "x"}, deployment.Annotations)
		require.NotNil(t, deployment.Spec.Replicas)
		assert.Equal(t, int32(1), *deployment.Spec.Replicas)
		assert.Equal(t, map[string]string{"app": "ctr"}, deployment.Spec.Selector.MatchLabels)
		require.NotNil(t, deployment.Spec.RevisionHistoryLimit)
		assert.Equal(t, int32(0), *deployment.Spec.RevisionHistoryLimit)
		assert.Equal(t, appsv1.RollingUpdateDeploymentStrategyType, deployment.Spec.Strategy.Type)

		pod := deployment.Spec.Template.Spec
		ctr := findContainer(pod, "ctr-fn")
		require.NotNil(t, ctr, "user container must be present")
		assert.Equal(t, "example/app:latest", ctr.Image)
		assert.Equal(t, apiv1.PullIfNotPresent, ctr.ImagePullPolicy)
		require.NotNil(t, pod.TerminationGracePeriodSeconds)
		assert.Equal(t, int64(30), *pod.TerminationGracePeriodSeconds)
	})

	t.Run("nil targetReplicas falls back to MinScale", func(t *testing.T) {
		cn := newTestContainer()
		fn := newTestContainerFunction()
		fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale = 2

		deployment, err := cn.getDeploymentSpec(t.Context(), fn, nil, "ctr-fn", "default", nil, nil)
		require.NoError(t, err)
		require.NotNil(t, deployment.Spec.Replicas)
		assert.Equal(t, int32(2), *deployment.Spec.Replicas)
	})

	t.Run("nil TerminationGracePeriodSeconds falls back to the default", func(t *testing.T) {
		cn := newTestContainer()
		fn := newTestContainerFunction()
		fn.Spec.PodSpec.TerminationGracePeriodSeconds = nil
		replicas := int32(1)

		deployment, err := cn.getDeploymentSpec(t.Context(), fn, &replicas, "ctr-fn", "default", nil, nil)
		require.NoError(t, err)

		pod := deployment.Spec.Template.Spec
		require.NotNil(t, pod.TerminationGracePeriodSeconds)
		assert.Equal(t, int64(6*60), *pod.TerminationGracePeriodSeconds)
	})

	t.Run("nil PodSpec returns an error instead of panicking", func(t *testing.T) {
		cn := newTestContainer()
		fn := newTestContainerFunction()
		fn.Spec.PodSpec = nil
		replicas := int32(1)

		_, err := cn.getDeploymentSpec(t.Context(), fn, &replicas, "ctr-fn", "default", nil, nil)
		require.Error(t, err)
	})

	t.Run("istio disables sidecar injection", func(t *testing.T) {
		cn := newTestContainer()
		cn.useIstio = true
		fn := newTestContainerFunction()
		replicas := int32(1)

		deployment, err := cn.getDeploymentSpec(t.Context(), fn, &replicas, "ctr-fn", "default", nil, nil)
		require.NoError(t, err)
		assert.Equal(t, "false", deployment.Spec.Template.Annotations["sidecar.istio.io/inject"])
	})

	t.Run("owner references are added when enabled", func(t *testing.T) {
		cn := newTestContainer()
		cn.enableOwnerReferences = true
		fn := newTestContainerFunction()
		replicas := int32(1)

		deployment, err := cn.getDeploymentSpec(t.Context(), fn, &replicas, "ctr-fn", "default", nil, nil)
		require.NoError(t, err)
		require.Len(t, deployment.OwnerReferences, 1)
		assert.Equal(t, "Function", deployment.OwnerReferences[0].Kind)
		assert.Equal(t, "ctr-fn", deployment.OwnerReferences[0].Name)
	})
}

// TestContainerFnDeleteCacheMiss verifies that deleting a function whose fsvc
// is absent from the in-memory cache (never specialized, evicted, or executor
// restarted) does not error out — it must fall back to the deterministic
// computed object name and clean up the backing objects instead of leaking
// them.
func TestContainerFnDeleteCacheMiss(t *testing.T) {
	logger := loggerfactory.GetLogger()
	cn := &Container{
		logger:           logger,
		kubernetesClient: fake.NewClientset(),
		fsCache:          fscache.MakeFunctionServiceCache(logger),
		nsResolver:       utils.DefaultNSResolver(),
		hpaops:           hpautils.NewHpaOperations(logger, fake.NewClientset(), "test-instance"),
	}

	fn := newTestContainerFunction()
	fn.UID = "abcdef01-2345-6789-abcd-ef0123456789"

	// fsCache is intentionally left empty so GetByFunctionUID misses.
	require.NoError(t, cn.fnDelete(t.Context(), fn),
		"fnDelete must tolerate a cache miss and clean up by computed name")
}

// TestContainerFnCreateDeletionGuard verifies the authoritative re-read at the
// top of fnCreate refuses to create backing objects for a Function that the
// router presented but which is gone or being deleted in the cluster. Without
// this guard an in-flight create can race the delete teardown and re-create the
// Deployment/Service/HPA after teardown removed them, leaking objects whose
// owning Function CR is already gone.
func TestContainerFnCreateDeletionGuard(t *testing.T) {
	t.Parallel()

	const liveUID = "abcdef01-2345-6789-abcd-ef0123456789"

	tests := []struct {
		name string
		// stored is the Function in the authoritative store (fake
		// fissionClient); nil means the function is absent.
		stored *fv1.Function
	}{
		{
			name:   "function absent from fissionClient",
			stored: nil,
		},
		{
			name: "function present but being deleted",
			stored: func() *fv1.Function {
				fn := newTestContainerFunction()
				fn.UID = liveUID
				fn.DeletionTimestamp = &metav1.Time{Time: time.Now()}
				fn.Finalizers = []string{"fission.io/test"}
				return fn
			}(),
		},
		{
			name: "function present but with a different UID",
			stored: func() *fv1.Function {
				fn := newTestContainerFunction()
				fn.UID = "00000000-0000-0000-0000-000000000000"
				return fn
			}(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			logger := loggerfactory.GetLogger()
			kubeClient := fake.NewClientset()
			fissionClient := fissionfake.NewSimpleClientset()
			if tc.stored != nil {
				fissionClient = fissionfake.NewSimpleClientset(tc.stored)
			}
			caaf := &Container{
				logger:           logger,
				kubernetesClient: kubeClient,
				fissionClient:    fissionClient,
				fsCache:          fscache.MakeFunctionServiceCache(logger),
				nsResolver:       utils.DefaultNSResolver(),
				hpaops:           hpautils.NewHpaOperations(logger, kubeClient, "test-instance"),
			}

			// The Function the router presents looks alive (no DeletionTimestamp).
			fn := newTestContainerFunction()
			fn.UID = liveUID

			_, err := caaf.fnCreate(t.Context(), fn)
			require.Error(t, err, "fnCreate must refuse a gone/deleting function")
			assert.True(t, ferror.IsNotFound(err),
				"guard must surface a ferror NotFound, got: %v", err)

			deploys, listErr := kubeClient.AppsV1().Deployments(metav1.NamespaceAll).List(t.Context(), metav1.ListOptions{})
			require.NoError(t, listErr)
			assert.Empty(t, deploys.Items,
				"no Deployment must be created when the function is gone/deleting")
		})
	}
}

// TestContainerFnCreateGuardPass verifies the guard lets a live, matching
// Function proceed past the re-read. We do not stand up a full happy-path
// (running pods); proving the guard passed is enough: fnCreate proceeds to
// create the backing objects and then fails waiting for the deployment to
// become available once the bounded context expires — a DeadlineExceeded
// error, which is not a ferror NotFound.
func TestContainerFnCreateGuardPass(t *testing.T) {
	t.Parallel()

	// A container function needs exactly one container port for the Service to
	// be created, so the create path can proceed past the guard to the
	// deployment wait.
	withPort := func(fn *fv1.Function) *fv1.Function {
		fn.Spec.PodSpec.Containers[0].Ports = []apiv1.ContainerPort{{ContainerPort: 8080}}
		return fn
	}

	live := withPort(newTestContainerFunction())
	live.UID = "abcdef01-2345-6789-abcd-ef0123456789"

	logger := loggerfactory.GetLogger()
	kubeClient := fake.NewClientset()
	caaf := &Container{
		logger:           logger,
		kubernetesClient: kubeClient,
		fissionClient:    fissionfake.NewSimpleClientset(live),
		fsCache:          fscache.MakeFunctionServiceCache(logger),
		nsResolver:       utils.DefaultNSResolver(),
		hpaops:           hpautils.NewHpaOperations(logger, kubeClient, "test-instance"),
	}

	fn := withPort(newTestContainerFunction())
	fn.UID = live.UID

	// Bound the context so the (never-ready, no real pods) deployment wait
	// returns promptly instead of blocking the default specialization timeout.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	_, err := caaf.fnCreate(ctx, fn)
	require.Error(t, err, "expected fnCreate to fail past the guard waiting for the deployment")
	assert.False(t, ferror.IsNotFound(err),
		"guard must have passed; the error should come from the deployment wait, not the guard. got: %v", err)
	// The non-NotFound error proves the guard passed: fnCreate proceeded past
	// the re-read into createOrGetDeployment and only failed at the bounded
	// deployment-readiness wait ("context deadline exceeded").
	assert.ErrorIs(t, err, context.DeadlineExceeded,
		"guard-pass should fail at the deployment wait, got: %v", err)
}

func TestContainerGetResources(t *testing.T) {
	cn := newTestContainer()
	fn := newTestContainerFunction()
	// no resource requests/limits set -> maps initialised, not nil
	res := cn.getResources(fn)
	assert.NotNil(t, res.Requests)
	assert.NotNil(t, res.Limits)
}
