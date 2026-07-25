// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

func TestGetMeta(t *testing.T) {
	t.Run("unversioned function prints no versioning line", func(t *testing.T) {
		fn := describeFunction()
		setDescribeClients(t, []runtime.Object{fn})

		out := captureStdout(t, func() error { return GetMeta(describeInput("hello", "default")) })

		assert.Contains(t, out, "Name: hello")
		assert.NotContains(t, out, "versioning:")
	})

	t.Run("versioning enabled adds a one-liner with the mode and version count", func(t *testing.T) {
		fn := describeFunction()
		fn.Spec.Versioning = &fv1.VersioningConfig{Mode: fv1.VersioningModeAuto}
		v1 := describeVersionObj("hello-v1", "hello", 1)
		v2 := describeVersionObj("hello-v2", "hello", 2)
		setDescribeClients(t, []runtime.Object{fn, v1, v2})

		out := captureStdout(t, func() error { return GetMeta(describeInput("hello", "default")) })

		assert.Contains(t, out, "versioning: auto (2 versions)")
	})

	t.Run("manual mode with no versions minted yet", func(t *testing.T) {
		fn := describeFunction()
		fn.Spec.Versioning = &fv1.VersioningConfig{Mode: fv1.VersioningModeManual}
		setDescribeClients(t, []runtime.Object{fn})

		out := captureStdout(t, func() error { return GetMeta(describeInput("hello", "default")) })

		assert.Contains(t, out, "versioning: manual (0 versions)")
	})

	t.Run("a failed versions List degrades the count to 0 rather than erroring getmeta", func(t *testing.T) {
		fn := describeFunction()
		fn.Spec.Versioning = &fv1.VersioningConfig{Mode: fv1.VersioningModeAuto}
		fc := fissionfake.NewClientset(fn)
		fc.PrependReactor("list", "functionversions", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, assert.AnError
		})
		cmd.ResetClientsetForTest()
		cmd.SetClientset(cmd.Client{FissionClientSet: fc, Namespace: "default"})

		out := captureStdout(t, func() error { return GetMeta(describeInput("hello", "default")) })

		assert.Contains(t, out, "versioning: auto (0 versions)")
	})

	t.Run("a missing function is still an error", func(t *testing.T) {
		setDescribeClients(t, []runtime.Object{describeFunction()})
		err := GetMeta(describeInput("does-not-exist", "default"))
		require.Error(t, err)
	})
}
