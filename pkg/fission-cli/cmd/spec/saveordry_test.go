// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

func captureSpecStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	orig := os.Stdout
	t.Cleanup(func() { os.Stdout = orig })
	os.Stdout = w
	runErr := fn()
	w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	require.NoError(t, runErr)
	return buf.String()
}

func TestSaveOrDry(t *testing.T) {
	fn := fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "ydry"}}

	t.Run("no spec flag is not handled", func(t *testing.T) {
		in := dummy.TestFlagSet()
		handled, err := SaveOrDry(in, fn, "fn.yaml")
		require.NoError(t, err)
		assert.False(t, handled)
	})

	t.Run("spec-dry prints the resource YAML", func(t *testing.T) {
		in := dummy.TestFlagSet()
		in.Set(flagkey.SpecDry, true)
		var handled bool
		out := captureSpecStdout(t, func() error {
			var err error
			handled, err = SaveOrDry(in, fn, "fn.yaml")
			return err
		})
		assert.True(t, handled)
		assert.Contains(t, out, "ydry")
	})
}
