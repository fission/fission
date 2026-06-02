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

func newCanary() *fv1.CanaryConfig {
	return &fv1.CanaryConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "canary", Namespace: "default"},
		Spec: fv1.CanaryConfigSpec{
			Trigger:                 "route",
			NewFunction:             "new",
			OldFunction:             "old",
			WeightIncrement:         10,
			WeightIncrementDuration: "2m",
			FailureThreshold:        10,
		},
		Status: fv1.CanaryConfigStatus{Status: "succeeded"},
	}
}

// setCanaryClient installs a fake clientset as the CLI's default client and
// returns it so the test can read resources back.
func setCanaryClient(objs ...*fv1.CanaryConfig) versioned.Interface {
	// NewSimpleClientset (legacy tracker) supports Update without an SSA schema;
	// NewClientset fails Update for fission CRs (no applyconfigs generated) with a
	// structured-merge-diff error. See kubernetes/kubernetes#126850.
	rs := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		rs = append(rs, o)
	}
	fc := fissionfake.NewSimpleClientset(rs...) //nolint:staticcheck
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})
	return fc
}

func TestCanaryUpdateResetsStatusOnChange(t *testing.T) {
	fc := setCanaryClient(newCanary())

	in := dummy.TestFlagSet()
	in.Set(flagkey.CanaryName, "canary")
	in.Set(flagkey.CanaryWeightIncrement, 20) // changed from 10
	in.Set(flagkey.CanaryFailureThreshold, 10)
	in.Set(flagkey.CanaryIncrementInterval, "2m")

	require.NoError(t, Update(in))

	got, err := fc.CoreV1().CanaryConfigs("default").Get(t.Context(), "canary", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, 20, got.Spec.WeightIncrement, "weight increment should be updated")
	assert.Equal(t, fv1.CanaryConfigStatusPending, got.Status.Status,
		"a changed canary config must reset status to pending so the controller re-evaluates")
}

func TestCanaryUpdateNoChangeKeepsStatus(t *testing.T) {
	fc := setCanaryClient(newCanary())

	in := dummy.TestFlagSet()
	in.Set(flagkey.CanaryName, "canary")
	in.Set(flagkey.CanaryWeightIncrement, 10) // same as existing
	in.Set(flagkey.CanaryFailureThreshold, 10)
	in.Set(flagkey.CanaryIncrementInterval, "2m")

	require.NoError(t, Update(in))

	got, err := fc.CoreV1().CanaryConfigs("default").Get(t.Context(), "canary", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "succeeded", got.Status.Status, "no spec change must leave status untouched")
}

func TestCanaryUpdateOnlySetFlagsMutate(t *testing.T) {
	fc := setCanaryClient(newCanary()) // step=10, threshold=10, duration=2m

	in := dummy.TestFlagSet()
	in.Set(flagkey.CanaryName, "canary")
	in.Set(flagkey.CanaryFailureThreshold, 5) // only this flag is provided

	require.NoError(t, Update(in))

	got, err := fc.CoreV1().CanaryConfigs("default").Get(t.Context(), "canary", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, 5, got.Spec.FailureThreshold, "the set flag should change")
	assert.Equal(t, 10, got.Spec.WeightIncrement, "an unset flag must not be clobbered with its default")
	assert.Equal(t, "2m", got.Spec.WeightIncrementDuration, "an unset flag must not be clobbered with its default")
	assert.Equal(t, fv1.CanaryConfigStatusPending, got.Status.Status)
}

func TestCanaryUpdateInvalidIntervalErrors(t *testing.T) {
	setCanaryClient(newCanary())

	in := dummy.TestFlagSet()
	in.Set(flagkey.CanaryName, "canary")
	in.Set(flagkey.CanaryIncrementInterval, "not-a-duration")

	require.Error(t, Update(in))
}

func TestCanaryUpdateMissingConfigErrors(t *testing.T) {
	setCanaryClient() // empty clientset

	in := dummy.TestFlagSet()
	in.Set(flagkey.CanaryName, "absent")
	in.Set(flagkey.CanaryIncrementInterval, "2m")

	require.Error(t, Update(in))
}
