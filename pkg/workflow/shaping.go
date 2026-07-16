// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"errors"
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/workflow/expr"
)

// errInvalidPath marks an unwritable ResultPath; the engine records it as a
// step failure with errorType Fission.InvalidPath.
var errInvalidPath = errors.New("invalid result path")

// shapeInput applies the state's InputPath ("" = identity) to its input
// document. A no-match reads as JSON null (the pinned dialect semantics).
func shapeInput(st fv1.WorkflowState, input any) (any, error) {
	if st.InputPath == "" {
		return input, nil
	}
	p, err := expr.Parse(st.InputPath)
	if err != nil {
		// Admission validates paths; reaching this means the snapshot predates
		// a dialect change — fail the step, never guess.
		return nil, fmt.Errorf("inputPath %q: %w", st.InputPath, err)
	}
	v, _ := p.Get(input) // no-match -> nil (JSON null)
	return v, nil
}

// shapeOutput merges the invocation result into the state's input document
// per ResultPath ("" = replace), then filters through OutputPath ("" =
// identity). An unwritable ResultPath wraps errInvalidPath.
func shapeOutput(st fv1.WorkflowState, input, result any) (any, error) {
	merged := result
	if st.ResultPath != "" {
		// Parse failures wrap errInvalidPath too: they are exactly as
		// permanent as an unwritable path, and anything else would loop a
		// SUCCEEDED function's side effects through endless re-invocation
		// (admission validates paths, but webhook-bypassed writes exist).
		p, err := expr.Parse(st.ResultPath)
		if err != nil {
			return nil, fmt.Errorf("%w: resultPath %q: %w", errInvalidPath, st.ResultPath, err)
		}
		merged, err = p.SetResult(input, result)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errInvalidPath, err)
		}
	}
	if st.OutputPath == "" {
		return merged, nil
	}
	p, err := expr.Parse(st.OutputPath)
	if err != nil {
		return nil, fmt.Errorf("%w: outputPath %q: %w", errInvalidPath, st.OutputPath, err)
	}
	v, _ := p.Get(merged) // no-match -> nil (JSON null)
	return v, nil
}

// evalChoice returns the first matching rule's Next, falling through to
// Default. ok=false means no rule matched and no Default exists —
// Fission.NoChoiceMatched upstream.
func evalChoice(st fv1.WorkflowState, input any) (string, bool) {
	for _, rule := range st.Choices {
		if evalRule(rule, input) {
			return rule.Next, true
		}
	}
	if st.Default != "" {
		return st.Default, true
	}
	return "", false
}

func evalRule(r fv1.WorkflowChoiceRule, input any) bool {
	switch {
	case len(r.And) > 0:
		for _, c := range r.And {
			if !evalCondition(c, input) {
				return false
			}
		}
		return true
	case len(r.Or) > 0:
		for _, c := range r.Or {
			if evalCondition(c, input) {
				return true
			}
		}
		return false
	case r.Not != nil:
		return !evalCondition(*r.Not, input)
	default:
		return evalCondition(r.WorkflowChoiceCondition, input)
	}
}

// evalCondition evaluates one leaf comparison against the input document.
// A malformed Variable (admission-validated, so effectively unreachable)
// evaluates to false rather than aborting the choice.
func evalCondition(c fv1.WorkflowChoiceCondition, input any) bool {
	p, err := expr.Parse(c.Variable)
	if err != nil {
		return false
	}
	v, matched := p.Get(input)

	switch {
	case c.IsPresent != nil:
		return matched == *c.IsPresent
	case c.IsNull != nil:
		return (matched && v == nil) == *c.IsNull
	case !matched:
		// Every remaining operator compares a value; a missing one never
		// matches (Step Functions parity).
		return false
	case c.StringEquals != nil:
		s, ok := v.(string)
		return ok && s == *c.StringEquals
	case c.BooleanEquals != nil:
		b, ok := v.(bool)
		return ok && b == *c.BooleanEquals
	case c.NumericEquals != nil:
		return numericCmp(v, c.NumericEquals) == 0
	case c.NumericGreaterThan != nil:
		return numericCmp(v, c.NumericGreaterThan) > 0
	case c.NumericLessThan != nil:
		return numericCmp(v, c.NumericLessThan) < 0
	default:
		return false // admission enforces exactly-one-operator
	}
}

// numericCmp compares a decoded JSON number against a Quantity; a non-number
// returns a sentinel that matches no comparison (2).
func numericCmp(v any, q *resource.Quantity) int {
	var s string
	switch n := v.(type) {
	case float64:
		s = strconv.FormatFloat(n, 'f', -1, 64)
	case int64:
		s = strconv.FormatInt(n, 10)
	default:
		return 2
	}
	vq, err := resource.ParseQuantity(s)
	if err != nil {
		return 2
	}
	return vq.Cmp(*q)
}
