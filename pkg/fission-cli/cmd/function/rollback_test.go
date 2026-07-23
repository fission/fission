// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

func rbIntPtr(i int) *int { return &i }

// aliasForRollback is name-pinned at hello-v3, with a two-entry History
// (most recent last, per the FunctionAliasStatus.History contract) recording
// hello-v1 then hello-v2 as prior targets — so a bare `fn rollback` (no
// --to) lands on hello-v2.
func aliasForRollback() *fv1.FunctionAlias {
	return &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec: fv1.FunctionAliasSpec{
			FunctionName: "hello",
			Version:      "hello-v3",
		},
		Status: fv1.FunctionAliasStatus{
			ResolvedVersion: "hello-v3",
			History: []fv1.AliasTargetRecord{
				{Version: "hello-v1", SwitchedAt: metav1.Now()},
				{Version: "hello-v2", SwitchedAt: metav1.Now()},
			},
		},
	}
}

func setRollbackClient(objs ...*fv1.FunctionAlias) *fissionfake.Clientset {
	rs := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		rs = append(rs, o)
	}
	fc := fissionfake.NewSimpleClientset(rs...) //nolint:staticcheck
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})
	return fc
}

func rollbackFlags(fnName, aliasName string) dummy.Cli {
	in := dummy.TestFlagSet()
	in.Set(flagkey.FnName, fnName)
	in.Set(flagkey.FnRollbackAlias, aliasName)
	return in
}

func TestRollbackToLastHistoryEntry(t *testing.T) {
	fc := setRollbackClient(aliasForRollback())

	in := rollbackFlags("hello", "prod")
	require.NoError(t, Rollback(in))

	got, err := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "hello-v2", got.Spec.Version, "no --to must roll back to History's last (most recent previous) entry")
	assert.Empty(t, got.Spec.PackageDigest)
}

func TestRollbackToExplicitTarget(t *testing.T) {
	fc := setRollbackClient(aliasForRollback())

	in := rollbackFlags("hello", "prod")
	in.Set(flagkey.FnRollbackTo, "hello-v1")
	require.NoError(t, Rollback(in))

	got, err := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "hello-v1", got.Spec.Version, "--to must win over History")
}

func TestRollbackEmptyHistoryErrorsWithoutTo(t *testing.T) {
	alias := aliasForRollback()
	alias.Status.History = nil
	setRollbackClient(alias)

	in := rollbackFlags("hello", "prod")
	err := Rollback(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no previous target recorded")
}

func TestRollbackWrongFunctionErrors(t *testing.T) {
	alias := aliasForRollback() // Spec.FunctionName == "hello"
	setRollbackClient(alias)

	in := rollbackFlags("other-function", "prod")
	err := Rollback(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "targets function 'hello'")

	// And the alias must be untouched.
	fc := setRollbackClient(alias)
	got, getErr := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.Equal(t, "hello-v3", got.Spec.Version)
}

func TestRollbackSpecManagedWarnsAndErrorsWithoutDetach(t *testing.T) {
	alias := aliasForRollback()
	alias.Annotations = map[string]string{spec.FISSION_DEPLOYMENT_UID_KEY: "deploy-uid-1"}
	fc := setRollbackClient(alias)

	in := rollbackFlags("hello", "prod")
	err := Rollback(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fission spec")
	assert.Contains(t, err.Error(), "--detach")

	got, getErr := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	require.NoError(t, getErr)
	assert.Equal(t, "hello-v3", got.Spec.Version, "spec-managed alias must not be repointed without --detach")
	assert.Equal(t, "deploy-uid-1", got.Annotations[spec.FISSION_DEPLOYMENT_UID_KEY])
}

func TestRollbackDetachStripsAnnotationsAndRepoints(t *testing.T) {
	alias := aliasForRollback()
	alias.Annotations = map[string]string{
		spec.FISSION_DEPLOYMENT_UID_KEY:  "deploy-uid-1",
		spec.FISSION_DEPLOYMENT_NAME_KEY: "myapp",
	}
	fc := setRollbackClient(alias)

	in := rollbackFlags("hello", "prod")
	in.Set(flagkey.FnRollbackDetach, true)
	require.NoError(t, Rollback(in))

	got, err := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "hello-v2", got.Spec.Version)
	assert.NotContains(t, got.Annotations, spec.FISSION_DEPLOYMENT_UID_KEY)
	assert.NotContains(t, got.Annotations, spec.FISSION_DEPLOYMENT_NAME_KEY)
}

func TestRollbackWeightedAliasClearsWeightAndSecondary(t *testing.T) {
	alias := aliasForRollback()
	alias.Spec.Weight = rbIntPtr(70)
	alias.Spec.SecondaryVersion = "hello-v3-canary"
	fc := setRollbackClient(alias)

	in := rollbackFlags("hello", "prod")
	require.NoError(t, Rollback(in))

	got, err := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "hello-v2", got.Spec.Version)
	assert.Nil(t, got.Spec.Weight, "rollback must stop a mid-canary traffic split")
	assert.Empty(t, got.Spec.SecondaryVersion)
}

func TestRollbackWaitSucceedsWhenAlreadyResolved(t *testing.T) {
	alias := aliasForRollback()
	// Simulate the resolver having already converged on the rollback target
	// by the time the polling loop's first Get lands.
	alias.Status.ResolvedVersion = "hello-v2"
	alias.Status.Conditions = []metav1.Condition{
		{Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionTrue, Reason: fv1.FunctionAliasReasonResolved},
	}
	setRollbackClient(alias)

	in := rollbackFlags("hello", "prod")
	in.Set(flagkey.FnRollbackWait, true)
	in.Set(flagkey.WaitTimeout, time.Second)
	require.NoError(t, Rollback(in))
}

func TestRollbackWaitTimesOutWhenUnresolved(t *testing.T) {
	alias := aliasForRollback()
	alias.Status.Conditions = []metav1.Condition{
		{Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionFalse, Reason: fv1.FunctionAliasReasonVersionNotFound},
	}
	setRollbackClient(alias)

	in := rollbackFlags("hello", "prod")
	in.Set(flagkey.FnRollbackWait, true)
	in.Set(flagkey.WaitTimeout, 20*time.Millisecond)

	err := Rollback(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

// TestRollbackWaitFlipsAfterRetries drives `fn rollback --wait` end to end
// against a fake clientset whose "get" reactor on functionaliases returns an
// unresolved alias for the first two Gets after the repoint Update, then a
// resolved one — exercising the actual poll-retry loop the CLI runs, not
// just a single-shot check.
func TestRollbackWaitFlipsAfterRetries(t *testing.T) {
	fc := setRollbackClient(aliasForRollback())

	var getsAfterUpdate atomic.Int32
	var updated atomic.Bool
	fc.PrependReactor("update", "functionaliases", func(action k8stesting.Action) (bool, runtime.Object, error) {
		updated.Store(true)
		return false, nil, nil // fall through to the default tracker update
	})
	fc.PrependReactor("get", "functionaliases", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if !updated.Load() {
			return false, nil, nil // pre-repoint Gets (complete() + UpdateOnConflict) pass through
		}
		n := getsAfterUpdate.Add(1)
		resolved := aliasForRollback()
		resolved.Spec.Version = "hello-v2"
		if n < 2 {
			resolved.Status.Conditions = []metav1.Condition{
				{Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionFalse, Reason: fv1.FunctionAliasReasonVersionNotFound},
			}
			return true, resolved, nil
		}
		resolved.Status.ResolvedVersion = "hello-v2"
		resolved.Status.Conditions = []metav1.Condition{
			{Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionTrue, Reason: fv1.FunctionAliasReasonResolved},
		}
		return true, resolved, nil
	})

	in := rollbackFlags("hello", "prod")
	in.Set(flagkey.FnRollbackWait, true)
	in.Set(flagkey.WaitTimeout, 5*time.Second)

	require.NoError(t, Rollback(in))
	assert.GreaterOrEqual(t, getsAfterUpdate.Load(), int32(2), "must have retried past the first unresolved poll")
}
