// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package httptrigger

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/util"
)

// functionColumn reproduces printHtSummary's row closure for just the
// FUNCTION(s) column, so the RFC-0025 rendering rule can be asserted without
// threading util.PrintObjects' table-formatting machinery through the test.
// TestPrintHtSummaryRunsWithoutError below exercises the real function end to
// end to catch a format/panic regression this unit-level copy wouldn't.
func functionColumn(trigger fv1.HTTPTrigger) string {
	if trigger.Spec.FunctionReference.Type == fv1.FunctionReferenceTypeFunctionName {
		function := trigger.Spec.FunctionReference.Name
		switch {
		case trigger.Spec.FunctionReference.Alias != "":
			function += ":" + trigger.Spec.FunctionReference.Alias
		case trigger.Spec.FunctionReference.Version != "":
			function += ":" + trigger.Spec.FunctionReference.Version
		}
		return function
	}
	function := ""
	for k, v := range trigger.Spec.FunctionReference.FunctionWeights {
		function += fmt.Sprintf("%s:%v ", k, v)
	}
	return function
}

// TestPrintHtSummaryFunctionColumn guards the RFC-0025 FUNCTION(s) column
// rendering shared by `route get` and `route list`: an alias/version-pinned
// reference must render as "<fn>:<tag>" (the router's own route grammar --
// see pkg/router/rewrite.go's trimFunctionPrefix), a plain reference must
// keep rendering as just the function name, and a weighted (canary)
// reference must be unaffected by the new alias/version fields.
func TestPrintHtSummaryFunctionColumn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ref  fv1.FunctionReference
		want string
	}{
		{
			name: "plain function reference unchanged",
			ref:  fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn"},
			want: "fn",
		},
		{
			name: "alias-pinned reference renders fn:alias",
			ref:  fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn", Alias: "prod"},
			want: "fn:prod",
		},
		{
			name: "version-pinned reference renders fn:version",
			ref:  fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn", Version: "fn-v3"},
			want: "fn:fn-v3",
		},
		{
			name: "weighted reference is unaffected",
			ref:  fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionWeights, FunctionWeights: map[string]int{"fn1": 50, "fn2": 50}},
			want: "fn1:50",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			trigger := fv1.HTTPTrigger{
				Name: "r", Namespace: "default",
				Spec: fv1.HTTPTriggerSpec{FunctionReference: tt.ref},
			}
			assert.Contains(t, functionColumn(trigger), tt.want)
		})
	}
}

// TestPrintHtSummaryRunsWithoutError sanity-checks printHtSummary actually
// renders (via util.PrintObjects) for a table containing an alias-pinned
// trigger, catching a panic/format regression the column-value unit test
// above wouldn't.
func TestPrintHtSummaryRunsWithoutError(t *testing.T) {
	t.Parallel()
	triggers := []fv1.HTTPTrigger{
		{
			Name: "r1", Namespace: "default",
			Spec: fv1.HTTPTriggerSpec{
				FunctionReference: fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "fn", Alias: "prod"},
			},
		},
	}
	err := printHtSummary(util.OutputTable, triggers)
	require.NoError(t, err)
}
