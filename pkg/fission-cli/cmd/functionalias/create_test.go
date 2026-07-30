// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package functionalias

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

func TestAliasCreateSetsOwnerRefToFunction(t *testing.T) {
	fn := &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default", UID: types.UID("fn-uid")},
	}
	fc := fissionfake.NewSimpleClientset(fn) //nolint:staticcheck
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

	in := dummy.TestFlagSet()
	in.Set(flagkey.AliasName, "prod")
	in.Set(flagkey.AliasFunction, "hello")
	in.Set(flagkey.AliasVersion, "hello-v1")

	require.NoError(t, Create(in))

	got, err := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "hello", got.Spec.FunctionName)
	assert.Equal(t, "hello-v1", got.Spec.Version)
	require.Len(t, got.OwnerReferences, 1)
	assert.Equal(t, "Function", got.OwnerReferences[0].Kind)
	assert.Equal(t, "hello", got.OwnerReferences[0].Name)
	assert.Equal(t, types.UID("fn-uid"), got.OwnerReferences[0].UID)
	assert.Equal(t, "hello", got.Labels[fv1.VersionFunctionNameLabel])
}

func TestAliasCreateMissingFunctionErrors(t *testing.T) {
	fc := fissionfake.NewSimpleClientset() //nolint:staticcheck
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

	in := dummy.TestFlagSet()
	in.Set(flagkey.AliasName, "prod")
	in.Set(flagkey.AliasFunction, "absent")
	in.Set(flagkey.AliasVersion, "v1")

	require.Error(t, Create(in))
}

func newAliasFunction() *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: "hello", Namespace: "default", UID: types.UID("fn-uid")},
	}
}

// TestAliasCreateWaitSucceedsAfterReactorFlips drives `alias create --wait`
// end to end against a fake clientset whose "get" reactor on functionaliases
// returns an unresolved alias for the first Get after Create and a resolved
// one from the second Get onward — exercising the actual poll-retry loop the
// CLI runs, not just a single-shot check (mirrors
// function.TestRollbackWaitFlipsAfterRetries, the other WaitForResolved
// caller).
func TestAliasCreateWaitSucceedsAfterReactorFlips(t *testing.T) {
	fc := fissionfake.NewSimpleClientset(newAliasFunction()) //nolint:staticcheck
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

	var gets atomic.Int32
	fc.PrependReactor("get", "functionaliases", func(action k8stesting.Action) (bool, runtime.Object, error) {
		n := gets.Add(1)
		alias := &fv1.FunctionAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
		}
		if n < 2 {
			alias.Status.Conditions = []metav1.Condition{
				{Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionFalse, Reason: fv1.FunctionAliasReasonVersionNotFound},
			}
			return true, alias, nil
		}
		alias.Status.ResolvedVersion = "hello-v1"
		alias.Status.Conditions = []metav1.Condition{
			{Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionTrue, Reason: fv1.FunctionAliasReasonResolved},
		}
		return true, alias, nil
	})

	in := dummy.TestFlagSet()
	in.Set(flagkey.AliasName, "prod")
	in.Set(flagkey.AliasFunction, "hello")
	in.Set(flagkey.AliasVersion, "hello-v1")
	in.Set(flagkey.AliasWait, true)
	in.Set(flagkey.WaitTimeout, 5*time.Second)

	require.NoError(t, Create(in))
	assert.GreaterOrEqual(t, gets.Load(), int32(2), "must have retried past the first unresolved poll")
}

func TestAliasCreateWaitTimesOutWhenUnresolved(t *testing.T) {
	fc := fissionfake.NewSimpleClientset(newAliasFunction()) //nolint:staticcheck
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

	fc.PrependReactor("get", "functionaliases", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, &fv1.FunctionAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
			Spec:       fv1.FunctionAliasSpec{FunctionName: "hello", Version: "hello-v1"},
			Status: fv1.FunctionAliasStatus{
				Conditions: []metav1.Condition{
					{Type: fv1.FunctionAliasConditionResolved, Status: metav1.ConditionFalse, Reason: fv1.FunctionAliasReasonVersionNotFound},
				},
			},
		}, nil
	})

	in := dummy.TestFlagSet()
	in.Set(flagkey.AliasName, "prod")
	in.Set(flagkey.AliasFunction, "hello")
	in.Set(flagkey.AliasVersion, "hello-v1")
	in.Set(flagkey.AliasWait, true)
	in.Set(flagkey.WaitTimeout, 20*time.Millisecond)

	err := Create(in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}

// TestAliasCreateCommandRegistersWaitFlags guards the flag wiring in
// command.go: --wait/--timeout must actually be registered on `alias
// create`, not just handled by run() if a caller happens to pass them.
func TestAliasCreateCommandRegistersWaitFlags(t *testing.T) {
	group := Commands()
	createCmd, _, err := group.Find([]string{"create"})
	require.NoError(t, err)
	assert.NotNil(t, createCmd.Flags().Lookup(flagkey.AliasWait), "--wait must be registered on `alias create`")
	assert.NotNil(t, createCmd.Flags().Lookup(flagkey.WaitTimeout), "--timeout must be registered on `alias create`")
}
