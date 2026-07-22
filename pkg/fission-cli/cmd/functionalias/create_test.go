// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package functionalias

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

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
