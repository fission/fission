// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package functionalias

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

func resolvedAlias(version string) *fv1.FunctionAlias {
	return &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", Version: version},
		Status: fv1.FunctionAliasStatus{
			ResolvedVersion: version,
			Conditions: []metav1.Condition{
				{Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionTrue, Reason: fv1.FunctionAliasReasonResolved},
			},
		},
	}
}

func unresolvedAlias(version string) *fv1.FunctionAlias {
	return &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", Version: version},
		Status: fv1.FunctionAliasStatus{
			Conditions: []metav1.Condition{
				{Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionFalse, Reason: fv1.FunctionAliasReasonVersionNotFound},
			},
		},
	}
}

func TestWaitForResolvedPublicAlreadyResolvedSucceeds(t *testing.T) {
	fc := fissionfake.NewSimpleClientset(resolvedAlias("hello-v1")) //nolint:staticcheck
	err := WaitForResolved(t.Context(), fc, "default", "prod", "hello-v1", time.Second)
	require.NoError(t, err)
}

func TestWaitForResolvedPublicWrongVersionTimesOut(t *testing.T) {
	// Resolved=True but to a different version than the caller wants —
	// mirrors a rollback's `--wait` racing a slower-than-expected resolver.
	fc := fissionfake.NewSimpleClientset(resolvedAlias("hello-v1")) //nolint:staticcheck
	err := WaitForResolved(t.Context(), fc, "default", "prod", "hello-v2", 20*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestWaitForResolvedPublicNeverResolvedTimesOut(t *testing.T) {
	fc := fissionfake.NewSimpleClientset(unresolvedAlias("hello-v1")) //nolint:staticcheck
	err := WaitForResolved(t.Context(), fc, "default", "prod", "hello-v1", 20*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

func TestWaitForResolvedEmptyWantVersionOnlyChecksResolvedCondition(t *testing.T) {
	// alias update --wait on a PackageDigest-pinned alias: the caller cannot
	// name the target version in advance, so wantVersion=="" degenerates to
	// "wait for Resolved=True only".
	fc := fissionfake.NewSimpleClientset(resolvedAlias("hello-v3")) //nolint:staticcheck
	err := WaitForResolved(t.Context(), fc, "default", "prod", "", time.Second)
	require.NoError(t, err)
}

// TestWaitForResolvedFlipsAfterRetries drives waitForResolved (the
// interval-parameterized core) against a fake clientset whose "get"
// reactor returns an unresolved alias for the first two Gets and a resolved
// one from the third Get onward — exercising the actual polling loop rather
// than a single-shot check.
func TestWaitForResolvedFlipsAfterRetries(t *testing.T) {
	fc := fissionfake.NewSimpleClientset(unresolvedAlias("hello-v1")) //nolint:staticcheck

	var gets atomic.Int32
	fc.PrependReactor("get", "functionaliases", func(action k8stesting.Action) (bool, runtime.Object, error) {
		n := gets.Add(1)
		if n < 3 {
			return true, unresolvedAlias("hello-v1"), nil
		}
		return true, resolvedAlias("hello-v1"), nil
	})

	get := func(ctx context.Context) (*fv1.FunctionAlias, error) {
		return fc.CoreV1().FunctionAliases("default").Get(ctx, "prod", metav1.GetOptions{})
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	err := waitForResolved(ctx, get, "hello-v1", 5*time.Millisecond)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, gets.Load(), int32(3), "must have retried past the first unresolved Get")
}

func TestWaitForResolvedPropagatesNonNotFoundError(t *testing.T) {
	assertErr := errors.New("boom")
	fc := fissionfake.NewSimpleClientset(unresolvedAlias("hello-v1")) //nolint:staticcheck
	fc.PrependReactor("get", "functionaliases", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, assertErr
	})

	get := func(ctx context.Context) (*fv1.FunctionAlias, error) {
		return fc.CoreV1().FunctionAliases("default").Get(ctx, "prod", metav1.GetOptions{})
	}

	err := waitForResolved(t.Context(), get, "hello-v1", 5*time.Millisecond)
	require.ErrorIs(t, err, assertErr)
}
