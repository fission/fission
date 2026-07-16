// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/fission/fission/pkg/workflow/expr"
)

// Workflow validation (RFC-0022). The graph and expression rules below are
// exactly what CEL cannot express (reachability needs a traversal, JSONPath
// needs a parser), so the admission webhooks enforce Validate() in full; the
// CLI's validate command and `fission spec validate` reuse the same methods.

const (
	// MaxWorkflowAttempts bounds per-state retry budgets. Workflow retries are
	// fold-level attempts recorded in the run's event stream (RFC-0022 W6),
	// not statestore-queue deliveries, so this is deliberately higher than
	// MaxAsyncAttempts.
	MaxWorkflowAttempts = 10
	// MaxWorkflowRunInputBytes caps WorkflowRun input (Step Functions parity;
	// etcd objects cap at ~1.5MiB). Larger inputs are passed by reference.
	MaxWorkflowRunInputBytes = 256 * 1024
	// MaxWorkflowStates bounds the graph size: an admission-time cost guard
	// that also keeps the reachability walk trivially cheap. Mirrored by the
	// maxProperties marker on WorkflowSpec.States.
	MaxWorkflowStates = 100
	// DefaultWorkflowTimeout is the run bound the engine applies when
	// spec.timeout is nil — a mis-authored graph or endlessly
	// caught-and-retried loop must not hold an active run forever.
	DefaultWorkflowTimeout = 24 * time.Hour
)

// wfStateNameRegexp pins the state-name grammar. Names become durable
// identifiers the moment a run starts (event-stream entries, activeStates,
// mermaid output), so the charset is pinned before they are un-renameable.
var wfStateNameRegexp = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// IsTerminal reports whether execution stops after this state — the single
// source of truth for "terminal" shared by the graph validator, the mermaid
// renderer, and (phase 2) the engine's fold.
func (st WorkflowState) IsTerminal() bool {
	return st.End || st.Type == WorkflowStateSucceed || st.Type == WorkflowStateFail
}

// ApplyDefaults fills the spec-level defaults the RFC's worked example
// relies on: a Task's function reference type defaults to "name" (the only
// other type, function-weights, is a canary concern that never applies to a
// workflow task). Called by the mutating webhook and the CLI manifest
// loader; the run-level Timeout default (DefaultWorkflowTimeout) is applied
// by the engine, not materialized into etcd.
func (spec *WorkflowSpec) ApplyDefaults() {
	for name, st := range spec.States {
		if st.Function != nil && st.Function.Name != "" && st.Function.Type == "" {
			st.Function.Type = FunctionReferenceTypeFunctionName
			spec.States[name] = st
		}
	}
}

func (spec WorkflowSpec) Validate() error {
	if len(spec.States) == 0 {
		return MakeValidationErr(ErrorInvalidValue, "WorkflowSpec.States", "", "at least one state is required")
	}

	var errs error
	if len(spec.States) > MaxWorkflowStates {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "WorkflowSpec.States", len(spec.States),
			fmt.Sprintf("at most %d states", MaxWorkflowStates)))
	}
	if spec.StartAt == "" {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "WorkflowSpec.StartAt", "", "required"))
	} else if _, ok := spec.States[spec.StartAt]; !ok {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "WorkflowSpec.StartAt", spec.StartAt,
			"does not name a declared state"))
	}
	if spec.Timeout != nil && spec.Timeout.Duration <= 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "WorkflowSpec.Timeout", spec.Timeout.Duration,
			"must be > 0"))
	}
	errs = errors.Join(errs, validateWorkflowRetry("WorkflowSpec.DefaultRetry", spec.DefaultRetry))

	for name, st := range spec.States {
		field := fmt.Sprintf("WorkflowSpec.States[%s]", name)
		if !wfStateNameRegexp.MatchString(name) {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field, name,
				"state names must match ^[A-Za-z0-9_-]{1,64}$ (they become durable identifiers in run history)"))
		}
		errs = errors.Join(errs, st.validate(field, spec.States))
	}

	// Graph-shape errors above (bad targets, malformed states) make a
	// reachability report noisy and misleading; only walk a well-formed graph.
	if errs == nil {
		errs = validateWorkflowGraph(spec)
	}
	return errs
}

// validate checks one state's per-type field exclusivity, target resolution,
// and expression syntax. states is the full graph, for Next/Default/Catch
// target checks.
func (st WorkflowState) validate(field string, states map[string]WorkflowState) error {
	var errs error

	target := func(f, name string) {
		if name == "" {
			return
		}
		if _, ok := states[name]; !ok {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+"."+f, name,
				"does not name a declared state"))
		}
	}
	jsonpath := func(f, path string) {
		if path == "" {
			return
		}
		if _, err := expr.Parse(path); err != nil {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+"."+f, path,
				fmt.Sprintf("invalid jsonpath: %v", err)))
		}
	}

	jsonpath("InputPath", st.InputPath)
	jsonpath("ResultPath", st.ResultPath)
	jsonpath("OutputPath", st.OutputPath)
	target("Next", st.Next)
	target("Default", st.Default)

	if st.Timeout != nil && st.Timeout.Duration <= 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".Timeout", st.Timeout.Duration,
			"must be > 0"))
	}

	seenCatch := map[string]bool{}
	for i, c := range st.Catch {
		cf := fmt.Sprintf("%s.Catch[%d]", field, i)
		if c.ErrorType == "" {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, cf+".ErrorType", "", "required"))
		} else if seenCatch[c.ErrorType] {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, cf+".ErrorType", c.ErrorType,
				"duplicate errorType; the first matching route wins, a duplicate is dead"))
		}
		seenCatch[c.ErrorType] = true
		target(fmt.Sprintf("Catch[%d].Next", i), c.Next)
		if c.Next == "" {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, cf+".Next", "", "required"))
		}
	}

	switch st.Type {
	case WorkflowStateTask:
		switch {
		case st.Function == nil:
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".Function", "",
				"required on a Task state"))
		case st.Function.Type != FunctionReferenceTypeFunctionName:
			// FunctionReference.Validate accepts function-weights (an
			// HTTPTrigger canary concern), but the engine can only execute a
			// by-name reference — reject at admission, not at run time.
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".Function.Type", st.Function.Type,
				"a Task function reference must be by name"))
		default:
			errs = errors.Join(errs, st.Function.Validate())
		}
		hasNext := st.Next != ""
		if hasNext == st.End {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field, st.Type,
				"a Task state sets exactly one of Next or End"))
		}
		if len(st.Choices) > 0 || st.Default != "" {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field, st.Type,
				"a Task state must not set Choices or Default"))
		}
		errs = errors.Join(errs, validateWorkflowRetry(field+".Retry", st.Retry))

	case WorkflowStateChoice:
		if len(st.Choices) == 0 {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".Choices", "",
				"a Choice state needs at least one rule"))
		}
		if st.Function != nil || st.Retry != nil || len(st.Catch) > 0 || st.Timeout != nil || st.Next != "" || st.End {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field, st.Type,
				"a Choice state must not set Function, Retry, Catch, Timeout, Next, or End (routing is via Choices/Default)"))
		}
		for i, rule := range st.Choices {
			errs = errors.Join(errs, rule.validate(fmt.Sprintf("%s.Choices[%d]", field, i)))
			target(fmt.Sprintf("Choices[%d].Next", i), rule.Next)
			if rule.Next == "" {
				errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, fmt.Sprintf("%s.Choices[%d].Next", field, i), "", "required"))
			}
		}

	case WorkflowStateSucceed, WorkflowStateFail:
		if st.Function != nil || st.Retry != nil || len(st.Catch) > 0 || st.Timeout != nil ||
			len(st.Choices) > 0 || st.Default != "" || st.Next != "" || st.End ||
			st.InputPath != "" || st.ResultPath != "" || st.OutputPath != "" {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field, st.Type,
				fmt.Sprintf("a %s state is terminal and carries no other fields", st.Type)))
		}

	default:
		errs = errors.Join(errs, MakeValidationErr(ErrorUnsupportedType, field+".Type", st.Type,
			"not a supported state type"))
	}

	return errs
}

// validate checks a choice rule: either a leaf comparison (inline) or exactly
// one of And/Or/Not over leaf conditions (depth-1 composition).
func (r WorkflowChoiceRule) validate(field string) error {
	composites := 0
	if len(r.And) > 0 {
		composites++
	}
	if len(r.Or) > 0 {
		composites++
	}
	if r.Not != nil {
		composites++
	}

	if r.WorkflowChoiceCondition != (WorkflowChoiceCondition{}) && composites > 0 {
		return MakeValidationErr(ErrorInvalidValue, field, "",
			"a rule is either a leaf comparison or a composite (and/or/not), not both")
	}

	switch composites {
	case 0:
		return r.WorkflowChoiceCondition.validate(field)
	case 1:
		var errs error
		for i, c := range r.And {
			errs = errors.Join(errs, c.validate(fmt.Sprintf("%s.And[%d]", field, i)))
		}
		for i, c := range r.Or {
			errs = errors.Join(errs, c.validate(fmt.Sprintf("%s.Or[%d]", field, i)))
		}
		if r.Not != nil {
			errs = errors.Join(errs, r.Not.validate(field+".Not"))
		}
		return errs
	default:
		return MakeValidationErr(ErrorInvalidValue, field, "",
			"a composite rule sets exactly one of And, Or, or Not")
	}
}

// validate checks a leaf condition: Variable required and parseable, exactly
// one comparison operator set.
func (c WorkflowChoiceCondition) validate(field string) error {
	var errs error

	if c.Variable == "" {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".Variable", "", "required"))
	} else if _, err := expr.Parse(c.Variable); err != nil {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".Variable", c.Variable,
			fmt.Sprintf("invalid jsonpath: %v", err)))
	}

	ops := 0
	for _, set := range []bool{
		c.StringEquals != nil,
		c.NumericEquals != nil,
		c.NumericGreaterThan != nil,
		c.NumericLessThan != nil,
		c.BooleanEquals != nil,
		c.IsPresent != nil,
		c.IsNull != nil,
	} {
		if set {
			ops++
		}
	}
	if ops != 1 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field, ops,
			"exactly one comparison operator must be set"))
	}
	return errs
}

// validateWorkflowRetry bounds a workflow retry policy: the shared
// RetryPolicy backoff ordering plus the workflow attempt cap. Deliberately
// not InvocationConfig.Validate: async delivery attempts are
// statestore-queue deliveries clamped to MaxAsyncAttempts, workflow attempts
// are fold-level.
func validateWorkflowRetry(field string, r *RetryPolicy) error {
	if r == nil {
		return nil
	}
	var errs error
	if r.MaxAttempts != nil && (*r.MaxAttempts < 1 || *r.MaxAttempts > MaxWorkflowAttempts) {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".MaxAttempts", *r.MaxAttempts,
			fmt.Sprintf("must be in [1, %d]", MaxWorkflowAttempts)))
	}
	return errors.Join(errs, r.validateBackoffBounds(field))
}

// validateWorkflowGraph walks the (individually well-formed) graph from
// StartAt and reports unreachable states and terminal unreachability. Cycles
// are legal — the run Timeout bounds them.
func validateWorkflowGraph(spec WorkflowSpec) error {
	if _, ok := spec.States[spec.StartAt]; !ok {
		return nil // already reported by the field check
	}

	edges := func(st WorkflowState) []string {
		var out []string
		if st.Next != "" {
			out = append(out, st.Next)
		}
		if st.Default != "" {
			out = append(out, st.Default)
		}
		for _, c := range st.Choices {
			if c.Next != "" {
				out = append(out, c.Next)
			}
		}
		for _, c := range st.Catch {
			if c.Next != "" {
				out = append(out, c.Next)
			}
		}
		return out
	}
	reached := map[string]bool{spec.StartAt: true}
	queue := []string{spec.StartAt}
	terminalReached := false
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		st := spec.States[name]
		if st.IsTerminal() {
			terminalReached = true
		}
		for _, next := range edges(st) {
			if !reached[next] {
				reached[next] = true
				queue = append(queue, next)
			}
		}
	}

	var errs error
	if !terminalReached {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "WorkflowSpec.States", "",
			"no terminal state (End, Succeed, or Fail) is reachable from StartAt"))
	}
	var unreachable []string
	for name := range spec.States {
		if !reached[name] {
			unreachable = append(unreachable, name)
		}
	}
	if len(unreachable) > 0 {
		slices.Sort(unreachable)
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "WorkflowSpec.States",
			strings.Join(unreachable, ", "), "unreachable from StartAt"))
	}
	return errs
}

// Validate is the full Workflow rule set — the admission webhook enforces it
// verbatim (nothing here is CEL-expressible: reachability needs a traversal,
// JSONPath needs a parser).
func (w *Workflow) Validate() error {
	return errors.Join(
		validateMetadata("Workflow", w.ObjectMeta),
		w.Spec.Validate())
}

func (wl *WorkflowList) Validate() error {
	var errs error
	for _, w := range wl.Items {
		errs = errors.Join(errs, w.Validate())
	}
	return errs
}

func (spec WorkflowRunSpec) Validate() error {
	var errs error
	if spec.WorkflowRef == "" {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "WorkflowRunSpec.WorkflowRef", "", "required"))
	} else {
		errs = errors.Join(errs, ValidateKubeName("WorkflowRunSpec.WorkflowRef", spec.WorkflowRef))
	}
	if spec.WorkflowGeneration < 0 {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "WorkflowRunSpec.WorkflowGeneration",
			spec.WorkflowGeneration, "must be >= 0"))
	}
	if spec.Input != nil && len(spec.Input.Raw) > MaxWorkflowRunInputBytes {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "WorkflowRunSpec.Input", len(spec.Input.Raw),
			fmt.Sprintf("must be <= %d bytes; pass large inputs by reference", MaxWorkflowRunInputBytes)))
	}
	return errs
}

// Validate is the full WorkflowRun rule set — the admission webhook enforces
// it verbatim (the input byte cap cannot be expressed in CEL: raw-bytes
// fields break CEL cost estimation).
func (wr *WorkflowRun) Validate() error {
	return errors.Join(
		validateMetadata("WorkflowRun", wr.ObjectMeta),
		wr.Spec.Validate())
}

func (wrl *WorkflowRunList) Validate() error {
	var errs error
	for _, wr := range wrl.Items {
		errs = errors.Join(errs, wr.Validate())
	}
	return errs
}
