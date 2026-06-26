// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

func TestUpdateOnConflictRetriesAndReapplies(t *testing.T) {
	t.Parallel()
	tt := &fv1.TimeTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "tt", Namespace: "default"},
		Spec:       fv1.TimeTriggerSpec{Cron: "@every 1h"},
	}
	cs := fissionfake.NewClientset(tt)

	// Fail the first Update with a 409 conflict, then let it through. The helper
	// must re-Get and re-Update — and the re-applied change must survive.
	var attempts int
	cs.PrependReactor("update", "timetriggers", func(action k8stesting.Action) (bool, runtime.Object, error) {
		attempts++
		if attempts == 1 {
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Group: "fission.io", Resource: "timetriggers"},
				"tt", errors.New("the object has been modified"))
		}
		// Echo the submitted object back (handled here rather than falling
		// through, since the fake tracker's apply/SMD path can't convert CRD
		// types).
		return true, action.(k8stesting.UpdateAction).GetObject(), nil
	})

	out, err := UpdateOnConflict(t.Context(), cs.CoreV1().TimeTriggers("default"), "tt",
		func(cur *fv1.TimeTrigger) { cur.Spec.Cron = "@every 2h" })

	require.NoError(t, err)
	assert.GreaterOrEqual(t, attempts, 2, "should have retried after the conflict")
	assert.Equal(t, "@every 2h", out.Spec.Cron, "re-applied change must be persisted")
}

func TestUpdateOnConflictPropagatesNonConflict(t *testing.T) {
	t.Parallel()
	cs := fissionfake.NewClientset()
	// Get of a missing object returns NotFound (not a conflict) — must surface,
	// not retry forever.
	_, err := UpdateOnConflict(t.Context(), cs.CoreV1().TimeTriggers("default"), "missing",
		func(*fv1.TimeTrigger) {})
	require.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err), "non-conflict errors propagate unchanged")
}
