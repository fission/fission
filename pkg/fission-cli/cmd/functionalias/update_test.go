// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package functionalias

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

func intPtr(i int) *int { return &i }

func newAlias() *fv1.FunctionAlias {
	return &fv1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "prod", Namespace: "default"},
		Spec: fv1.FunctionAliasSpec{
			FunctionName: "hello",
			Version:      "hello-v1",
		},
	}
}

// setAliasClient installs a fake clientset as the CLI's default client and
// returns it so the test can read resources back. NewSimpleClientset (legacy
// tracker) supports Update without an SSA schema; NewClientset fails Update
// for fission CRs (no applyconfigs generated) — see canaryconfig's identical
// helper for the upstream issue reference.
func setAliasClient(objs ...*fv1.FunctionAlias) versioned.Interface {
	rs := make([]runtime.Object, 0, len(objs))
	for _, o := range objs {
		rs = append(rs, o)
	}
	fc := fissionfake.NewSimpleClientset(rs...) //nolint:staticcheck
	cmd.ResetClientsetForTest()
	cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})
	return fc
}

func TestAliasUpdateOnlySetFlagsMutate(t *testing.T) {
	fc := setAliasClient(newAlias()) // version=hello-v1

	in := dummy.TestFlagSet()
	in.Set(flagkey.AliasName, "prod")
	// no other flags set

	require.NoError(t, Update(in))

	got, err := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "hello-v1", got.Spec.Version, "an unset flag must not clobber the existing target")
	assert.Nil(t, got.Spec.Weight)
}

func TestAliasUpdateVersionClearsPackageDigest(t *testing.T) {
	alias := newAlias()
	alias.Spec.Version = ""
	alias.Spec.PackageDigest = "sha256:" + fixedDigest()
	fc := setAliasClient(alias)

	in := dummy.TestFlagSet()
	in.Set(flagkey.AliasName, "prod")
	in.Set(flagkey.AliasVersion, "hello-v2")

	require.NoError(t, Update(in))

	got, err := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "hello-v2", got.Spec.Version)
	assert.Empty(t, got.Spec.PackageDigest, "setting --version must clear a previously set --package-digest (XOR pin)")
}

func TestAliasUpdatePackageDigestClearsVersion(t *testing.T) {
	fc := setAliasClient(newAlias()) // version=hello-v1, no digest

	in := dummy.TestFlagSet()
	in.Set(flagkey.AliasName, "prod")
	digest := "sha256:" + fixedDigest()
	in.Set(flagkey.AliasPackageDigest, digest)

	require.NoError(t, Update(in))

	got, err := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, digest, got.Spec.PackageDigest)
	assert.Empty(t, got.Spec.Version, "setting --package-digest must clear a previously set --version (XOR pin)")
}

func TestAliasUpdateWeightAndSecondaryVersion(t *testing.T) {
	fc := setAliasClient(newAlias())

	in := dummy.TestFlagSet()
	in.Set(flagkey.AliasName, "prod")
	in.Set(flagkey.AliasWeight, 80)
	in.Set(flagkey.AliasSecondaryVersion, "hello-v2")

	require.NoError(t, Update(in))

	got, err := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, got.Spec.Weight)
	assert.Equal(t, 80, *got.Spec.Weight)
	assert.Equal(t, "hello-v2", got.Spec.SecondaryVersion)
}

func TestAliasUpdateClearWeightDropsSplit(t *testing.T) {
	alias := newAlias()
	alias.Spec.Weight = intPtr(50)
	alias.Spec.SecondaryVersion = "hello-v2"
	fc := setAliasClient(alias)

	in := dummy.TestFlagSet()
	in.Set(flagkey.AliasName, "prod")
	in.Set(flagkey.AliasClearWeight, true)

	require.NoError(t, Update(in))

	got, err := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Nil(t, got.Spec.Weight)
	assert.Empty(t, got.Spec.SecondaryVersion)
}

func TestAliasUpdateClearWeightWinsOverWeightInSameCall(t *testing.T) {
	fc := setAliasClient(newAlias())

	in := dummy.TestFlagSet()
	in.Set(flagkey.AliasName, "prod")
	in.Set(flagkey.AliasWeight, 30)
	in.Set(flagkey.AliasSecondaryVersion, "hello-v2")
	in.Set(flagkey.AliasClearWeight, true)

	require.NoError(t, Update(in))

	got, err := fc.CoreV1().FunctionAliases("default").Get(t.Context(), "prod", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Nil(t, got.Spec.Weight, "--clear-weight must win over --weight passed in the same call")
	assert.Empty(t, got.Spec.SecondaryVersion)
}

func TestAliasUpdateMissingAliasErrors(t *testing.T) {
	setAliasClient() // empty clientset

	in := dummy.TestFlagSet()
	in.Set(flagkey.AliasName, "absent")

	require.Error(t, Update(in))
}

func fixedDigest() string {
	return "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"
}
