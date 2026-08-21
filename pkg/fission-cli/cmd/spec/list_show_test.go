// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package spec

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// captureShow runs fn with os.Stdout redirected and returns what it wrote.
func captureShow(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	orig := os.Stdout
	t.Cleanup(func() { os.Stdout = orig })
	os.Stdout = w
	fn()
	w.Close()
	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)
	return buf.String()
}

// TestShowSections pins the `spec list` summary rendering through the shared
// showSection helper: the title line, the tab-aligned header/rows, the
// trailing blank line separating sections, and — load-bearing — that an empty
// kind prints nothing at all rather than a bare title.
func TestShowSections(t *testing.T) {
	t.Run("an empty kind prints nothing", func(t *testing.T) {
		assert.Empty(t, captureShow(t, func() { ShowTimeTriggers(nil) }))
		assert.Empty(t, captureShow(t, func() { ShowFunctions([]fv1.Function{}) }))
	})

	t.Run("time triggers render title, aligned columns, and a trailing blank line", func(t *testing.T) {
		tts := []fv1.TimeTrigger{
			{Name: "hourly", Spec: fv1.TimeTriggerSpec{Cron: "@hourly", FunctionReference: fv1.FunctionReference{Name: "fn-a"}}},
			{Name: "m", Spec: fv1.TimeTriggerSpec{Cron: "* * * * *", FunctionReference: fv1.FunctionReference{Name: "fn-b"}}},
		}
		tts[0].Spec.Name = "fn-a"
		tts[1].Spec.Name = "fn-b"

		out := captureShow(t, func() { ShowTimeTriggers(tts) })

		assert.Equal(t,
			"Time Triggers:\n"+
				"NAME   CRON      FUNCTION_NAME\n"+
				"hourly @hourly   fn-a\n"+
				"m      * * * * * fn-b\n"+
				"\n",
			out)
	})

	t.Run("workflows render the state count as a cell", func(t *testing.T) {
		wfs := []fv1.Workflow{{Name: "wf"}}
		wfs[0].Spec.StartAt = "s1"
		wfs[0].Spec.States = map[string]fv1.WorkflowState{"s1": {}, "s2": {}}

		out := captureShow(t, func() { ShowWorkflows(wfs) })

		assert.Contains(t, out, "Workflows:\n")
		assert.Contains(t, out, "NAME STARTAT STATES\n")
		assert.Contains(t, out, "wf   s1      2\n")
	})
}
