// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func qty(t *testing.T, s string) *resource.Quantity {
	t.Helper()
	q, err := resource.ParseQuantity(s)
	require.NoError(t, err)
	return &q
}

func TestShapeInput(t *testing.T) {
	t.Parallel()

	input := map[string]any{"order": map[string]any{"id": "4711"}, "noise": true}

	got, err := shapeInput(fv1.WorkflowState{InputPath: "$.order"}, input)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"id": "4711"}, got)

	got, err = shapeInput(fv1.WorkflowState{}, input)
	require.NoError(t, err)
	assert.Equal(t, input, got, "empty InputPath is identity")

	got, err = shapeInput(fv1.WorkflowState{InputPath: "$.missing"}, input)
	require.NoError(t, err)
	assert.Nil(t, got, "no-match reads as JSON null")
}

func TestShapeOutput(t *testing.T) {
	t.Parallel()

	input := map[string]any{"order": map[string]any{"id": "4711"}}
	result := map[string]any{"txn": "t-1"}

	t.Run("resultPath merges into input (RFC worked example)", func(t *testing.T) {
		t.Parallel()
		got, err := shapeOutput(fv1.WorkflowState{ResultPath: "$.charge"}, input, result)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{
			"order":  map[string]any{"id": "4711"},
			"charge": map[string]any{"txn": "t-1"},
		}, got)
	})

	t.Run("empty resultPath replaces", func(t *testing.T) {
		t.Parallel()
		got, err := shapeOutput(fv1.WorkflowState{}, input, result)
		require.NoError(t, err)
		assert.Equal(t, result, got)
	})

	t.Run("outputPath filters after merge", func(t *testing.T) {
		t.Parallel()
		got, err := shapeOutput(fv1.WorkflowState{ResultPath: "$.charge", OutputPath: "$.charge.txn"}, input, result)
		require.NoError(t, err)
		assert.Equal(t, "t-1", got)
	})

	t.Run("unwritable resultPath is errInvalidPath", func(t *testing.T) {
		t.Parallel()
		_, err := shapeOutput(fv1.WorkflowState{ResultPath: "$.order.id.deep"}, input, result)
		require.ErrorIs(t, err, errInvalidPath)
	})
}

func TestEvalChoice(t *testing.T) {
	t.Parallel()

	leaf := func(variable string, mutate func(*fv1.WorkflowChoiceCondition)) fv1.WorkflowChoiceCondition {
		c := fv1.WorkflowChoiceCondition{Variable: variable}
		mutate(&c)
		return c
	}

	input := map[string]any{
		"amount":  float64(42),
		"tier":    "gold",
		"active":  true,
		"nullish": nil,
	}

	cases := []struct {
		name string
		cond fv1.WorkflowChoiceCondition
		want bool
	}{
		{"numericEquals int-vs-float", leaf("$.amount", func(c *fv1.WorkflowChoiceCondition) { c.NumericEquals = qty(t, "42") }), true},
		{"numericGreaterThan", leaf("$.amount", func(c *fv1.WorkflowChoiceCondition) { c.NumericGreaterThan = qty(t, "41.5") }), true},
		{"numericLessThan false", leaf("$.amount", func(c *fv1.WorkflowChoiceCondition) { c.NumericLessThan = qty(t, "42") }), false},
		{"stringEquals", leaf("$.tier", func(c *fv1.WorkflowChoiceCondition) { c.StringEquals = new("gold") }), true},
		{"stringEquals wrong type", leaf("$.amount", func(c *fv1.WorkflowChoiceCondition) { c.StringEquals = new("42") }), false},
		{"booleanEquals", leaf("$.active", func(c *fv1.WorkflowChoiceCondition) { c.BooleanEquals = new(true) }), true},
		{"isPresent true", leaf("$.tier", func(c *fv1.WorkflowChoiceCondition) { c.IsPresent = new(true) }), true},
		{"isPresent false on missing", leaf("$.ghost", func(c *fv1.WorkflowChoiceCondition) { c.IsPresent = new(false) }), true},
		{"isNull on explicit null", leaf("$.nullish", func(c *fv1.WorkflowChoiceCondition) { c.IsNull = new(true) }), true},
		{"isNull on missing is false", leaf("$.ghost", func(c *fv1.WorkflowChoiceCondition) { c.IsNull = new(true) }), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, evalCondition(tc.cond, input))
		})
	}

	t.Run("first match wins, then default, then no-match", func(t *testing.T) {
		t.Parallel()
		st := fv1.WorkflowState{
			Type: fv1.WorkflowStateChoice,
			Choices: []fv1.WorkflowChoiceRule{
				{WorkflowChoiceCondition: leaf("$.amount", func(c *fv1.WorkflowChoiceCondition) { c.NumericGreaterThan = qty(t, "100") }), Next: "big"},
				{WorkflowChoiceCondition: leaf("$.tier", func(c *fv1.WorkflowChoiceCondition) { c.StringEquals = new("gold") }), Next: "vip"},
			},
			Default: "std",
		}
		next, ok := evalChoice(st, input)
		require.True(t, ok)
		assert.Equal(t, "vip", next)

		st.Choices = st.Choices[:1] // no rule matches now
		next, ok = evalChoice(st, input)
		require.True(t, ok)
		assert.Equal(t, "std", next, "falls through to Default")

		st.Default = ""
		_, ok = evalChoice(st, input)
		assert.False(t, ok, "no match and no default -> Fission.NoChoiceMatched upstream")
	})

	t.Run("and or not composition", func(t *testing.T) {
		t.Parallel()
		gt40 := leaf("$.amount", func(c *fv1.WorkflowChoiceCondition) { c.NumericGreaterThan = qty(t, "40") })
		lt50 := leaf("$.amount", func(c *fv1.WorkflowChoiceCondition) { c.NumericLessThan = qty(t, "50") })
		gt100 := leaf("$.amount", func(c *fv1.WorkflowChoiceCondition) { c.NumericGreaterThan = qty(t, "100") })

		and := fv1.WorkflowChoiceRule{And: []fv1.WorkflowChoiceCondition{gt40, lt50}, Next: "in-range"}
		or := fv1.WorkflowChoiceRule{Or: []fv1.WorkflowChoiceCondition{gt100, lt50}, Next: "either"}
		not := fv1.WorkflowChoiceRule{Not: &gt100, Next: "not-big"}

		st := fv1.WorkflowState{Type: fv1.WorkflowStateChoice, Choices: []fv1.WorkflowChoiceRule{and}}
		next, ok := evalChoice(st, input)
		require.True(t, ok)
		assert.Equal(t, "in-range", next)

		st.Choices = []fv1.WorkflowChoiceRule{or}
		next, ok = evalChoice(st, input)
		require.True(t, ok)
		assert.Equal(t, "either", next)

		st.Choices = []fv1.WorkflowChoiceRule{not}
		next, ok = evalChoice(st, input)
		require.True(t, ok)
		assert.Equal(t, "not-big", next)
	})
}
