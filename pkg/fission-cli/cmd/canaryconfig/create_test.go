// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package canaryconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

// setCreateClient installs a fake clientset seeded with objs (of any
// resource type) as the CLI's default client and returns it so the test can
// read resources back. Mirrors setCanaryClient (update_test.go) but is not
// restricted to *fv1.CanaryConfig — create's alias-mode branch reads
// HTTPTriggers, FunctionAliases, and FunctionVersions too.
func setCreateClient(objs ...runtime.Object) versioned.Interface {
	fc := fissionfake.NewSimpleClientset(objs...) //nolint:staticcheck
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})
	return fc
}

func newCreateFlagSet(name, trigger, newFn, oldFn string) dummy.Cli {
	in := dummy.TestFlagSet()
	in.Set(flagkey.CanaryName, name)
	in.Set(flagkey.CanaryHTTPTriggerName, trigger)
	in.Set(flagkey.CanaryNewFunc, newFn)
	in.Set(flagkey.CanaryOldFunc, oldFn)
	in.Set(flagkey.CanaryWeightIncrement, 20)
	in.Set(flagkey.CanaryFailureThreshold, 10)
	in.Set(flagkey.CanaryIncrementInterval, "2m")
	return in
}

func weightedTrigger(name string, weights map[string]int) *fv1.HTTPTrigger {
	t := &fv1.HTTPTrigger{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	t.Spec.FunctionReference.Type = fv1.FunctionReferenceTypeFunctionWeights
	t.Spec.FunctionReference.FunctionWeights = weights
	return t
}

func aliasTrigger(name, alias string) *fv1.HTTPTrigger {
	t := &fv1.HTTPTrigger{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	t.Spec.FunctionReference.Type = fv1.FunctionReferenceTypeFunctionName
	t.Spec.FunctionReference.Alias = alias
	return t
}

func newFunctionVersion(name, fnName string, seq int64) *fv1.FunctionVersion {
	return &fv1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       fv1.FunctionVersionSpec{FunctionName: fnName, Sequence: seq},
	}
}

func TestCanaryCreateFunctionWeightsMode(t *testing.T) {
	trigger := weightedTrigger("route", map[string]int{"new": 0, "old": 100})
	fc := setCreateClient(trigger,
		&fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "default"}},
		&fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: "default"}},
	)

	in := newCreateFlagSet("canary", "route", "new", "old")
	require.NoError(t, Create(in))

	got, err := fc.CoreV1().CanaryConfigs("default").Get(t.Context(), "canary", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "new", got.Spec.NewFunction)
	assert.Equal(t, "old", got.Spec.OldFunction)
}

func TestCanaryCreateFunctionWeightsModeMissingWeightEntryErrors(t *testing.T) {
	// "new" is not in the trigger's FunctionWeights map.
	trigger := weightedTrigger("route", map[string]int{"old": 100})
	setCreateClient(trigger,
		&fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "default"}},
		&fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: "default"}},
	)

	in := newCreateFlagSet("canary", "route", "new", "old")
	require.Error(t, Create(in))
}

func TestCanaryCreateAliasMode(t *testing.T) {
	trigger := aliasTrigger("route", "prod")
	alias := &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: "orders", Version: "orders-v1"},
	}
	oldVer := newFunctionVersion("orders-v1", "orders", 1)
	newVer := newFunctionVersion("orders-v2", "orders", 2)
	fc := setCreateClient(trigger, alias, oldVer, newVer)

	in := newCreateFlagSet("canary", "route", "orders-v2", "orders-v1")
	require.NoError(t, Create(in))

	got, err := fc.CoreV1().CanaryConfigs("default").Get(t.Context(), "canary", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "orders-v2", got.Spec.NewFunction)
	assert.Equal(t, "orders-v1", got.Spec.OldFunction)
}

func TestCanaryCreateAliasModeNotPointingAtOldFuncErrors(t *testing.T) {
	trigger := aliasTrigger("route", "prod")
	alias := &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: "orders", Version: "orders-v0"}, // not --oldfn
	}
	oldVer := newFunctionVersion("orders-v1", "orders", 1)
	newVer := newFunctionVersion("orders-v2", "orders", 2)
	fc := setCreateClient(trigger, alias, oldVer, newVer)

	in := newCreateFlagSet("canary", "route", "orders-v2", "orders-v1")
	require.Error(t, Create(in))

	_, err := fc.CoreV1().CanaryConfigs("default").Get(t.Context(), "canary", metav1.GetOptions{})
	assert.Error(t, err, "a rejected create must not leave a CanaryConfig behind")
}

func TestCanaryCreateAliasModeMissingAliasErrors(t *testing.T) {
	trigger := aliasTrigger("route", "prod") // "prod" alias not seeded
	oldVer := newFunctionVersion("orders-v1", "orders", 1)
	newVer := newFunctionVersion("orders-v2", "orders", 2)
	setCreateClient(trigger, oldVer, newVer)

	in := newCreateFlagSet("canary", "route", "orders-v2", "orders-v1")
	require.Error(t, Create(in))
}

func TestCanaryCreateAliasModeMissingVersionErrors(t *testing.T) {
	trigger := aliasTrigger("route", "prod")
	alias := &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: "orders", Version: "orders-v1"},
	}
	oldVer := newFunctionVersion("orders-v1", "orders", 1)
	// "orders-v2" not seeded.
	setCreateClient(trigger, alias, oldVer)

	in := newCreateFlagSet("canary", "route", "orders-v2", "orders-v1")
	require.Error(t, Create(in))
}

func TestCanaryCreateAliasModeVersionBelongsToDifferentFunctionErrors(t *testing.T) {
	trigger := aliasTrigger("route", "prod")
	alias := &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: "orders", Version: "orders-v1"},
	}
	oldVer := newFunctionVersion("orders-v1", "orders", 1)
	newVer := newFunctionVersion("payments-v1", "payments", 1) // wrong function
	setCreateClient(trigger, alias, oldVer, newVer)

	in := newCreateFlagSet("canary", "route", "payments-v1", "orders-v1")
	require.Error(t, Create(in))
}

func TestCanaryCreateAliasModeSkipsWeightsMapCheck(t *testing.T) {
	// An alias-referencing trigger has no FunctionWeights map at all; create
	// must not fall into the function-weights validation path.
	trigger := aliasTrigger("route", "prod")
	assert.Nil(t, trigger.Spec.FunctionReference.FunctionWeights)
	alias := &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec:       fv1.FunctionAliasSpec{FunctionName: "orders", Version: "orders-v1"},
	}
	oldVer := newFunctionVersion("orders-v1", "orders", 1)
	newVer := newFunctionVersion("orders-v2", "orders", 2)
	fc := setCreateClient(trigger, alias, oldVer, newVer)

	in := newCreateFlagSet("canary", "route", "orders-v2", "orders-v1")
	require.NoError(t, Create(in))

	_, err := fc.CoreV1().CanaryConfigs("default").Get(t.Context(), "canary", metav1.GetOptions{})
	require.NoError(t, err)
}
