// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dchest/uniuri"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/workqueue"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	fClient "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// newReaperGpm builds a GenericPoolManager wired with fakes and the given
// Manager-cache objects, suitable for driving the reap handler directly.
func newReaperGpm(t *testing.T, crObjs ...client.Object) *GenericPoolManager {
	t.Helper()
	logger := loggerfactory.GetLogger()
	kubernetesClient := fake.NewClientset()
	fissionClient := fClient.NewClientset()
	metricsClient := metricsclient.NewSimpleClientset() //nolint:staticcheck
	fcfg, err := fetcherConfig.MakeFetcherConfig("/userfunc")
	require.NoError(t, err)
	executor, err := MakeGenericPoolManager(t.Context(), logger,
		fissionClient, kubernetesClient, metricsClient,
		fcfg, strings.ToLower(uniuri.NewLen(8)), nil)
	require.NoError(t, err)
	gpm := executor.(*GenericPoolManager)
	gpm.crClient = crfake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(crObjs...).Build()
	return gpm
}

// addTestPool drops a hand-built pool into gpm.pools and creates its backing
// deployment in the fake clientset, so destroy() has something to delete.
func addTestPool(t *testing.T, gpm *GenericPoolManager, envName, imageHash string, idleFor time.Duration) string {
	t.Helper()
	env := &fv1.Environment{ObjectMeta: metav1.ObjectMeta{
		Name: envName, Namespace: metav1.NamespaceDefault, UID: types.UID("env-uid-" + envName),
	}}
	deployName := fmt.Sprintf("pool-%s-%s", envName, imageHash)
	_, err := gpm.kubernetesClient.AppsV1().Deployments(metav1.NamespaceDefault).Create(t.Context(),
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: deployName, Namespace: metav1.NamespaceDefault}},
		metav1.CreateOptions{})
	require.NoError(t, err)
	pool := &GenericPool{
		logger:           gpm.logger,
		env:              env,
		fnNamespace:      metav1.NamespaceDefault,
		deployment:       &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: deployName, Namespace: metav1.NamespaceDefault}},
		kubernetesClient: gpm.kubernetesClient,
		readyPodQueue:    workqueue.NewTypedDelayingQueueWithConfig(workqueue.TypedDelayingQueueConfig[string]{Name: "test"}),
		ociImageHash:     imageHash,
	}
	pool.lastActive.Store(time.Now().Add(-idleFor).UnixNano())
	key := poolKey(env.UID, imageHash)
	gpm.pools[key] = pool
	gpm.readyPodQueues.Store(key, pool.readyPodQueue)
	return key
}

func poolPod(name, imageHash, managed string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: metav1.NamespaceDefault,
			Labels: map[string]string{
				fv1.POOL_OCI_IMAGE_HASH: imageHash,
				"managed":               managed,
			},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

// TestReapIdlePoolsDecisionTable pins the reap-eligibility contract.
func TestReapIdlePoolsDecisionTable(t *testing.T) {
	const hash = "abcd1234efgh5678"

	cases := []struct {
		name      string
		imageHash string // "" = generic pool
		idleFor   time.Duration
		pods      []client.Object
		wantReap  bool
	}{
		{
			name:      "generic pool is never reaped, however idle",
			imageHash: "",
			idleFor:   24 * time.Hour,
			wantReap:  false,
		},
		{
			name:      "fresh per-image pool stays",
			imageHash: hash,
			idleFor:   time.Second,
			wantReap:  false,
		},
		{
			name:      "idle per-image pool with a specialized pod stays",
			imageHash: hash,
			idleFor:   time.Hour,
			pods:      []client.Object{poolPod("specialized", hash, "false", corev1.PodRunning)},
			wantReap:  false,
		},
		{
			name:      "idle per-image pool with only warm pods is reaped",
			imageHash: hash,
			idleFor:   time.Hour,
			pods:      []client.Object{poolPod("warm", hash, "true", corev1.PodRunning)},
			wantReap:  true,
		},
		{
			name:      "idle per-image pool with only a terminated specialized pod is reaped",
			imageHash: hash,
			idleFor:   time.Hour,
			pods:      []client.Object{poolPod("dead", hash, "false", corev1.PodSucceeded)},
			wantReap:  true,
		},
		{
			name:      "specialized pod of ANOTHER pool does not pin this one",
			imageHash: hash,
			idleFor:   time.Hour,
			pods:      []client.Object{poolPod("other", "ffff0000ffff0000", "false", corev1.PodRunning)},
			wantReap:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gpm := newReaperGpm(t, tc.pods...)
			key := addTestPool(t, gpm, "env", tc.imageHash, tc.idleFor)
			deployName := gpm.pools[key].deployment.Name

			gpm.handleReapIdlePools(&request{ctx: t.Context()})

			_, stillThere := gpm.pools[key]
			assert.Equal(t, !tc.wantReap, stillThere, "pool map entry")
			_, qThere := gpm.readyPodQueues.Load(key)
			assert.Equal(t, !tc.wantReap, qThere, "ready pod queue entry")
			_, err := gpm.kubernetesClient.AppsV1().Deployments(metav1.NamespaceDefault).Get(t.Context(), deployName, metav1.GetOptions{})
			if tc.wantReap {
				assert.Error(t, err, "the pool deployment must be deleted")
			} else {
				assert.NoError(t, err, "the pool deployment must survive")
			}
		})
	}
}

// TestReapIdlePoolsFailSafeOnListError pins the uncertainty rule: if the pod
// list fails, the pool is NOT reaped (the next pass retries).
func TestReapIdlePoolsFailSafeOnListError(t *testing.T) {
	gpm := newReaperGpm(t)
	gpm.crClient = crfake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				return fmt.Errorf("injected list failure")
			},
		}).Build()
	key := addTestPool(t, gpm, "env", "abcd1234efgh5678", time.Hour)

	gpm.handleReapIdlePools(&request{ctx: t.Context()})

	_, stillThere := gpm.pools[key]
	assert.True(t, stillThere, "a pool must never be reaped on listing uncertainty")
}

// TestGetPoolTouchesActivityClock pins the freshness contract: every GET_POOL
// (the first step of every specialization) bumps lastActive, and because the
// actor serializes GET_POOL with REAP_IDLE_POOLS, a just-fetched pool cannot
// be reaped within the idle window.
func TestGetPoolTouchesActivityClock(t *testing.T) {
	gpm := newReaperGpm(t)
	// Register the pool under the exact key handleGetPool derives for this
	// archive, so the GET takes the found path (no pool creation involved).
	oci := &ociPoolSpec{archive: &fv1.OCIArchive{Image: "reg.example/pkg:v1"}}
	key := addTestPool(t, gpm, "env", ociPoolHash(oci), time.Hour)
	pool := gpm.pools[key]
	stale := pool.lastActive.Load()

	respC := make(chan *response, 1)
	gpm.handleGetPool(&request{
		ctx:             t.Context(),
		env:             pool.env,
		oci:             oci,
		responseChannel: respC,
	})
	resp := <-respC
	require.NoError(t, resp.error)
	require.Same(t, pool, resp.pool, "the existing pool must be found, not recreated")

	assert.Greater(t, pool.lastActive.Load(), stale, "GET_POOL must bump the activity clock")

	// A freshly-touched pool must survive a reap pass even with a long-idle
	// window already elapsed before the touch (the serialized-actor guarantee
	// the comment in handleGetPool promises).
	gpm.handleReapIdlePools(&request{ctx: t.Context()})
	_, stillThere := gpm.pools[key]
	assert.True(t, stillThere)
}

// TestOCIPoolIdleReapTimeFromEnv pins the soft-fail config contract.
func TestOCIPoolIdleReapTimeFromEnv(t *testing.T) {
	logger := loggerfactory.GetLogger()
	cases := []struct {
		value string
		want  time.Duration
	}{
		{value: "", want: defaultOCIPoolIdleReapTime},
		{value: "10m", want: 10 * time.Minute},
		{value: "garbage", want: defaultOCIPoolIdleReapTime},
		{value: "-5m", want: defaultOCIPoolIdleReapTime},
		{value: "0", want: defaultOCIPoolIdleReapTime},
	}
	for _, tc := range cases {
		t.Run("value="+tc.value, func(t *testing.T) {
			t.Setenv("OCI_POOL_IDLE_REAP_TIME", tc.value)
			assert.Equal(t, tc.want, ociPoolIdleReapTimeFromEnv(logger))
		})
	}
}
