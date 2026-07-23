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
	// MaxWorkflowBranchStates bounds each branch graph (mirrored by the
	// maxProperties marker; tighter than top-level so doubly-nested CEL
	// rules stay under the apiserver's cost budget).
	MaxWorkflowBranchStates = 20
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

// IsTerminal reports whether execution stops after this branch state. It
// delegates to WorkflowState.IsTerminal via ToState so "terminal" has exactly
// one definition — the two levels cannot drift.
func (st WorkflowBranchState) IsTerminal() bool {
	return st.ToState().IsTerminal()
}

// Terminal reports whether the run phase is final.
func (p WorkflowRunPhase) Terminal() bool {
	switch p {
	case WorkflowRunSucceeded, WorkflowRunFailed, WorkflowRunCancelled, WorkflowRunTimedOut:
		return true
	default:
		return false
	}
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
		}
		// Branch states carry the same function references one level down —
		// a default that skips them makes every Parallel/Map manifest that
		// omits function.type fail validation.
		for bi := range st.Branches {
			for bn, bst := range st.Branches[bi].States {
				if bst.Function != nil && bst.Function.Name != "" && bst.Function.Type == "" {
					bst.Function.Type = FunctionReferenceTypeFunctionName
					st.Branches[bi].States[bn] = bst
				}
			}
		}
		spec.States[name] = st
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
		jsonpath(fmt.Sprintf("Catch[%d].ResultPath", i), c.ResultPath)
	}

	errs = errors.Join(errs, st.validateFieldExclusivity(field))

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
			// RFC-0025 explicitly defers a Task state's live-tracking
			// alias/version semantics ("WorkflowState.Function must not grow
			// alias fields until it is decided", rfc/0025-function-versions-
			// aliases-rollback.md) — whether a run should resolve alias→version
			// once at RunStarted (version-consistent) or re-resolve per step
			// (live-tracking) changes replay semantics and isn't settled.
			// FunctionReference.Validate() itself accepts both fields (they're
			// legitimate for HTTPTrigger/TimeTrigger/MessageQueueTrigger/
			// KubernetesWatchTrigger), and the CRD schema's struct-level CEL
			// rules on FunctionReference accept them too regardless of where
			// the type is embedded — so this workflow-specific Go-side check
			// is the ONLY thing rejecting them for a Task state, and it is
			// reached only through the webhook (pkg/webhook/workflow.go calls
			// Workflow.Validate(), which reaches WorkflowSpec.Validate() and
			// this state validator), not CEL.
			if st.Function.Alias != "" || st.Function.Version != "" {
				errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".Function", st.Function.Name,
					"alias/version references on a workflow Task state are not yet supported (RFC-0025 defers this decision until live-tracking-vs-version-consistent run semantics are settled)"))
			}
		}
		hasNext := st.Next != ""
		if hasNext == st.End {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field, st.Type,
				"a Task state sets exactly one of Next or End"))
		}
		errs = errors.Join(errs, validateWorkflowRetry(field+".Retry", st.Retry))

	case WorkflowStateChoice:
		if len(st.Choices) == 0 {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".Choices", "",
				"a Choice state needs at least one rule"))
		}
		for i, rule := range st.Choices {
			errs = errors.Join(errs, rule.validate(fmt.Sprintf("%s.Choices[%d]", field, i)))
			target(fmt.Sprintf("Choices[%d].Next", i), rule.Next)
			if rule.Next == "" {
				errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, fmt.Sprintf("%s.Choices[%d].Next", field, i), "", "required"))
			}
		}

	case WorkflowStateParallel, WorkflowStateMap:
		hasNext := st.Next != ""
		if hasNext == st.End {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field, st.Type,
				fmt.Sprintf("a %s state sets exactly one of Next or End", st.Type)))
		}
		if st.Type == WorkflowStateMap {
			if st.ItemsPath == "" {
				errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".ItemsPath", "",
					"required on a Map state"))
			}
			if len(st.Branches) != 1 {
				errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".Branches", len(st.Branches),
					"a Map state carries exactly one branch (the iterator template)"))
			}
		} else if len(st.Branches) == 0 {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".Branches", "",
				"a Parallel state needs at least one branch"))
		}
		for i, b := range st.Branches {
			errs = errors.Join(errs, b.validate(fmt.Sprintf("%s.Branches[%d]", field, i)))
		}

	case WorkflowStateWait:
		if st.Duration == nil || st.Duration.Duration <= 0 {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".Duration", st.Duration,
				"a Wait state needs a positive duration"))
		}
		hasNext := st.Next != ""
		if hasNext == st.End {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field, st.Type,
				"a Wait state sets exactly one of Next or End"))
		}

	case WorkflowStateSucceed, WorkflowStateFail:
		// Terminal shape enforced entirely by the exclusivity table.

	default:
		errs = errors.Join(errs, MakeValidationErr(ErrorUnsupportedType, field+".Type", st.Type,
			"not a supported state type"))
	}

	return errs
}

// stateField declares one WorkflowState field and which state types may set
// it. EVERY new field added to WorkflowState gets exactly one row here —
// missing a row means the field is silently admitted on every type, which is
// how junk fields creep into durable spec snapshots.
type stateField struct {
	name      string
	isSet     func(WorkflowState) bool
	allowedOn map[WorkflowStateType]bool
}

var (
	onTask       = map[WorkflowStateType]bool{WorkflowStateTask: true}
	onWait       = map[WorkflowStateType]bool{WorkflowStateWait: true}
	onChoice     = map[WorkflowStateType]bool{WorkflowStateChoice: true}
	onFanOut     = map[WorkflowStateType]bool{WorkflowStateParallel: true, WorkflowStateMap: true}
	onTaskFanOut = map[WorkflowStateType]bool{WorkflowStateTask: true, WorkflowStateParallel: true, WorkflowStateMap: true}
	onNexting    = map[WorkflowStateType]bool{WorkflowStateTask: true, WorkflowStateParallel: true, WorkflowStateMap: true, WorkflowStateWait: true}
)

var stateFields = []stateField{
	{"Function", func(s WorkflowState) bool { return s.Function != nil }, onTask},
	{"Timeout", func(s WorkflowState) bool { return s.Timeout != nil }, onTask},
	// Retry stays Task-only: no region-retry in v1 — re-running a whole
	// Parallel/Map fan-out on failure re-executes every branch's side
	// effects; a Catch route is the failure surface instead.
	{"Retry", func(s WorkflowState) bool { return s.Retry != nil }, onTask},
	{"Catch", func(s WorkflowState) bool { return len(s.Catch) > 0 }, onTaskFanOut},
	{"Choices", func(s WorkflowState) bool { return len(s.Choices) > 0 }, onChoice},
	{"Default", func(s WorkflowState) bool { return s.Default != "" }, onChoice},
	{"Branches", func(s WorkflowState) bool { return len(s.Branches) > 0 }, onFanOut},
	{"ItemsPath", func(s WorkflowState) bool { return s.ItemsPath != "" },
		map[WorkflowStateType]bool{WorkflowStateMap: true}},
	{"MaxConcurrency", func(s WorkflowState) bool { return s.MaxConcurrency != 0 }, onFanOut},
	{"InputPath", func(s WorkflowState) bool { return s.InputPath != "" }, onTaskFanOut},
	{"ResultPath", func(s WorkflowState) bool { return s.ResultPath != "" }, onTaskFanOut},
	{"OutputPath", func(s WorkflowState) bool { return s.OutputPath != "" }, onTaskFanOut},
	{"Duration", func(s WorkflowState) bool { return s.Duration != nil }, onWait},
	{"Next", func(s WorkflowState) bool { return s.Next != "" }, onNexting},
	{"End", func(s WorkflowState) bool { return s.End }, onNexting},
}

// validateFieldExclusivity rejects fields set on a state type that does not
// carry them (a Choice with a Function, a Succeed with a Next, ...). One
// declarative pass replaces per-type hand-enumerated "must not set" lists.
func (st WorkflowState) validateFieldExclusivity(field string) error {
	var errs error
	for _, f := range stateFields {
		if f.isSet(st) && !f.allowedOn[st.Type] {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+"."+f.name, st.Type,
				fmt.Sprintf("a %s state must not set %s", st.Type, f.name)))
		}
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

// ToState widens a branch state for the engine and validators; the fan-out
// fields stay zero (impossible by type).
func (b WorkflowBranchState) ToState() WorkflowState {
	return WorkflowState{
		Type: b.Type, Function: b.Function, Duration: b.Duration, Timeout: b.Timeout,
		Retry: b.Retry, Catch: b.Catch, Choices: b.Choices, Default: b.Default,
		InputPath: b.InputPath, ResultPath: b.ResultPath, OutputPath: b.OutputPath,
		Next: b.Next, End: b.End,
	}
}

// StatesAsWorkflow widens the branch's state map for validator/engine reuse.
func (b WorkflowBranch) StatesAsWorkflow() map[string]WorkflowState {
	out := make(map[string]WorkflowState, len(b.States))
	for name, bs := range b.States {
		out[name] = bs.ToState()
	}
	return out
}

// validate checks one branch: its states are validated with the same rules
// as top-level states (widened via ToState — nested fan-out is impossible by
// type, and the widened Type would fail the enum check anyway), then the
// same reachability walk.
func (b WorkflowBranch) validate(field string) error {
	var errs error
	states := b.StatesAsWorkflow()
	if len(states) > MaxWorkflowBranchStates {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".States", len(states),
			fmt.Sprintf("at most %d states per branch", MaxWorkflowBranchStates)))
	}
	if b.StartAt == "" {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".StartAt", "", "required"))
	} else if _, ok := states[b.StartAt]; !ok {
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".StartAt", b.StartAt,
			"does not name a declared state"))
	}
	for name, st := range states {
		sf := fmt.Sprintf("%s.States[%s]", field, name)
		if !wfStateNameRegexp.MatchString(name) {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, sf, name,
				"state names must match ^[A-Za-z0-9_-]{1,64}$ (they become durable identifiers in run history)"))
		}
		// The bounded branch type cannot CARRY fan-out fields, but the Type
		// enum string is shared — reject the type explicitly with a message
		// that says why, not a confusing "needs at least one branch".
		if st.Type == WorkflowStateParallel || st.Type == WorkflowStateMap {
			errs = errors.Join(errs, MakeValidationErr(ErrorUnsupportedType, sf+".Type", st.Type,
				"nested fan-out is not supported: a branch state cannot be Parallel or Map"))
			continue
		}
		errs = errors.Join(errs, st.validate(sf, states))
	}
	if errs == nil {
		errs = validateGraph(field, b.StartAt, states)
	}
	return errs
}

// validateWorkflowGraph walks the (individually well-formed) graph from
// StartAt and reports unreachable states and terminal unreachability. Cycles
// are legal — the run Timeout bounds them.
func validateWorkflowGraph(spec WorkflowSpec) error {
	return validateGraph("WorkflowSpec", spec.StartAt, spec.States)
}

// validateGraph is the shared walk for the top-level machine and each branch.
func validateGraph(field, startAt string, states map[string]WorkflowState) error {
	if _, ok := states[startAt]; !ok {
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
	reached := map[string]bool{startAt: true}
	queue := []string{startAt}
	terminalReached := false
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		st := states[name]
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
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".States", "",
			"no terminal state (End, Succeed, or Fail) is reachable from StartAt"))
	}
	var unreachable []string
	for name := range states {
		if !reached[name] {
			unreachable = append(unreachable, name)
		}
	}
	if len(unreachable) > 0 {
		slices.Sort(unreachable)
		errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, field+".States",
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
