// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestMermaidFromSpec(t *testing.T) {
	t.Parallel()

	spec := fv1.WorkflowSpec{
		StartAt: "a",
		States: map[string]fv1.WorkflowState{
			"a": {
				Type:     fv1.WorkflowStateTask,
				Function: &fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn"},
				Next:     "c",
				Catch:    []fv1.WorkflowCatchRoute{{ErrorType: fv1.WorkflowErrAll, Next: "fail"}},
			},
			"c": {
				Type: fv1.WorkflowStateChoice,
				Choices: []fv1.WorkflowChoiceRule{{
					WorkflowChoiceCondition: fv1.WorkflowChoiceCondition{Variable: "$.ok", IsPresent: new(true)},
					Next:                    "done",
				}},
				Default: "fail",
			},
			"done": {Type: fv1.WorkflowStateSucceed},
			"fail": {Type: fv1.WorkflowStateFail},
		},
	}

	out, _ := renderMermaid(spec, nil)

	assert.Contains(t, out, "stateDiagram-v2")
	assert.Contains(t, out, "[*] --> a")
	assert.Contains(t, out, "a --> c")
	assert.Contains(t, out, "a --> fail : Fission.All")
	assert.Contains(t, out, "c --> done : rule 1")
	assert.Contains(t, out, "c --> fail : default")
	assert.Contains(t, out, "done --> [*]")
	assert.Contains(t, out, "fail --> [*]")

	// Deterministic output: same spec, same rendering.
	again, _ := renderMermaid(spec, nil)
	assert.Equal(t, out, again)
}
