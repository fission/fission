// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqtrigger

import (
	"testing"

	kedafake "github.com/kedacore/keda/v2/pkg/generated/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// mqtForKind builds a minimal-but-valid MessageQueueTrigger of the given kind.
// It carries no Secret so createKedaObjects exercises the create path without an
// AuthenticationTrigger, keeping the keda fake interaction to ScaledObject only.
func mqtForKind(name, kind string) *fv1.MessageQueueTrigger {
	pollingInterval := int32(30)
	cooldownPeriod := int32(300)
	minReplicaCount := int32(0)
	maxReplicaCount := int32(100)
	return &fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: metav1.NamespaceDefault,
			UID:       types.UID(name + "-uid"),
		},
		Spec: fv1.MessageQueueTriggerSpec{
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "test-func",
			},
			MessageQueueType: fv1.MessageQueueTypeKafka,
			Topic:            "topic",
			PollingInterval:  &pollingInterval,
			CooldownPeriod:   &cooldownPeriod,
			MinReplicaCount:  &minReplicaCount,
			MaxReplicaCount:  &maxReplicaCount,
			MqtKind:          kind,
		},
	}
}

// newTestScalerReconciler wires a scalerReconciler over fake clients: the cache
// client is seeded with objs, and the keda/kube side effects land in returnable
// fakes so the test can assert on the resulting KEDA objects.
func newTestScalerReconciler(t *testing.T, objs ...client.Object) (*scalerReconciler, *kedafake.Clientset) {
	t.Helper()
	kedaClient := kedafake.NewSimpleClientset()
	r := newScalerReconciler(
		loggerfactory.GetLogger(),
		crfake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build(),
		kedaClient,
		k8sfake.NewClientset(),
		"http://router.fission",
	)
	return r, kedaClient
}

func TestScalerReconciler(t *testing.T) {
	key := types.NamespacedName{Name: "mqt", Namespace: metav1.NamespaceDefault}
	req := ctrl.Request{NamespacedName: key}

	getScaledObject := func(t *testing.T, kc *kedafake.Clientset) (bool, error) {
		t.Helper()
		_, err := kc.KedaV1alpha1().ScaledObjects(metav1.NamespaceDefault).Get(t.Context(), "mqt", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return err == nil, err
	}

	t.Run("first sight of a keda trigger creates the scaled object and caches it", func(t *testing.T) {
		t.Parallel()
		r, kc := newTestScalerReconciler(t, mqtForKind("mqt", MqtKindKeda))

		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)

		exists, err := getScaledObject(t, kc)
		require.NoError(t, err)
		assert.True(t, exists, "keda trigger should create a ScaledObject")
		assert.NotNil(t, r.getLastSeen(key), "last-seen should be cached after a successful create")
	})

	t.Run("non-keda trigger is skipped, no keda objects created", func(t *testing.T) {
		t.Parallel()
		r, kc := newTestScalerReconciler(t, mqtForKind("mqt", MqtKindFission))

		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)

		exists, err := getScaledObject(t, kc)
		require.NoError(t, err)
		assert.False(t, exists, "fission-kind trigger must not create a ScaledObject")
		assert.NotNil(t, r.getLastSeen(key), "last-seen is cached even for skipped fission triggers")
	})

	t.Run("unchanged keda trigger on re-reconcile is a no-op", func(t *testing.T) {
		t.Parallel()
		mqt := mqtForKind("mqt", MqtKindKeda)
		r, kc := newTestScalerReconciler(t, mqt)

		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		// Second reconcile with the identical spec: checkAndUpdateTriggerFields
		// returns false, so no update side effects, last-seen still present.
		_, err = r.Reconcile(t.Context(), req)
		require.NoError(t, err)

		exists, err := getScaledObject(t, kc)
		require.NoError(t, err)
		assert.True(t, exists)
		assert.NotNil(t, r.getLastSeen(key))
	})

	t.Run("deleted trigger forgets its last-seen entry", func(t *testing.T) {
		t.Parallel()
		// Empty cache client: Get returns NotFound (the MQT is gone).
		r, _ := newTestScalerReconciler(t)
		r.setLastSeen(key, mqtForKind("mqt", MqtKindKeda))

		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		assert.Nil(t, r.getLastSeen(key), "NotFound should drop the last-seen entry")
	})

	t.Run("keda to fission transition tears down and re-caches", func(t *testing.T) {
		t.Parallel()
		// Cache now holds a fission-kind MQT; last-seen says it was keda.
		r, _ := newTestScalerReconciler(t, mqtForKind("mqt", MqtKindFission))
		r.setLastSeen(key, mqtForKind("mqt", MqtKindKeda))

		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
		last := r.getLastSeen(key)
		require.NotNil(t, last)
		assert.Equal(t, MqtKindFission, last.Spec.MqtKind, "last-seen should advance to the fission spec after teardown")
	})

	t.Run("fission to keda transition creates the scaled object", func(t *testing.T) {
		t.Parallel()
		// Cache holds a keda-kind MQT; last-seen says it was fission.
		r, kc := newTestScalerReconciler(t, mqtForKind("mqt", MqtKindKeda))
		r.setLastSeen(key, mqtForKind("mqt", MqtKindFission))

		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)

		exists, err := getScaledObject(t, kc)
		require.NoError(t, err)
		assert.True(t, exists, "fission->keda transition should create a ScaledObject")
		last := r.getLastSeen(key)
		require.NotNil(t, last)
		assert.Equal(t, MqtKindKeda, last.Spec.MqtKind)
	})

	t.Run("non-secret update on a secret-bearing trigger keeps the AuthenticationRef", func(t *testing.T) {
		t.Parallel()
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "kafka-secret", Namespace: metav1.NamespaceDefault},
			Data:       map[string][]byte{"password": []byte("s3cr3t")},
		}
		mqt := mqtForKind("mqt", MqtKindKeda)
		mqt.Spec.Secret = "kafka-secret"

		kc := kedafake.NewSimpleClientset()
		r := newScalerReconciler(
			loggerfactory.GetLogger(),
			crfake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(mqt).Build(),
			kc,
			k8sfake.NewClientset(secret),
			"http://router.fission",
		)

		// First reconcile creates the ScaledObject with its AuthenticationRef.
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)

		// Change a NON-secret field (max replicas) and re-reconcile. The secret is
		// unchanged, so the update path must still carry the AuthenticationRef
		// through to the ScaledObject instead of stripping it.
		cur := &fv1.MessageQueueTrigger{}
		require.NoError(t, r.client.Get(t.Context(), key, cur))
		newMax := int32(7)
		cur.Spec.MaxReplicaCount = &newMax
		require.NoError(t, r.client.Update(t.Context(), cur))

		_, err = r.Reconcile(t.Context(), req)
		require.NoError(t, err)

		so, err := kc.KedaV1alpha1().ScaledObjects(metav1.NamespaceDefault).Get(t.Context(), "mqt", metav1.GetOptions{})
		require.NoError(t, err)
		require.Len(t, so.Spec.Triggers, 1)
		require.NotNil(t, so.Spec.Triggers[0].AuthenticationRef, "AuthenticationRef must survive a non-secret update")
		assert.Equal(t, authTriggerName("mqt"), so.Spec.Triggers[0].AuthenticationRef.Name)
	})
}
