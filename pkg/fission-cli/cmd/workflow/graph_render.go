// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// nodeStatus is a state's run-derived status. A node absent from an overlay is
// unreached.
type nodeStatus string

const (
	statusOK        nodeStatus = "ok"
	statusFailed    nodeStatus = "failed"
	statusActive    nodeStatus = "active"
	statusUnreached nodeStatus = "unreached"
)

// idUnsafe matches everything mermaid will not accept inside a node id. State
// names may contain '-' (the CRD allows ^[A-Za-z0-9_-]{1,64}$), which mermaid's
// stateDiagram parser chokes on, so ids are sanitized and the original name
// rides along as the display label.
var idUnsafe = regexp.MustCompile(`[^A-Za-z0-9_]`)

// mermaidID joins parts into a mermaid-safe node id.
func mermaidID(parts ...string) string {
	return idUnsafe.ReplaceAllString(strings.Join(parts, "__"), "_")
}

// mapTemplateBranch is the single region a Map is drawn as (see
// renderedBranches): its branches are per-item instances of one template, so
// the diagram draws region 0 and eventNodeID folds every item's events onto it.
const mapTemplateBranch = "0"

// branchNodeID is the one place the branch-node id shape is defined: it scopes
// a branch state's id under its region by branch key. Branch state names are
// only unique within their branch — a top-level state (or another region) may
// reuse the name, and mermaid ids are global. The renderer keys by the region
// index (strconv.Itoa(i)) and the overlay by the wire Branch field; both are
// the same string, so both must build the id through here or coloring drifts.
func branchNodeID(parent, branch, name string) string {
	return mermaidID(parent, branch, name)
}

// typeClassPrefix marks a class as a state-type class ("wftask") rather than a
// run-status class ("ok"); the two share one class namespace, and the legend
// tells them apart by this prefix.
const typeClassPrefix = "wf"

func typeClass(t fv1.WorkflowStateType) string {
	return typeClassPrefix + strings.ToLower(string(t))
}

// isTypeClass reports whether c is a state-type class (vs a run-status class).
func isTypeClass(c string) bool { return strings.HasPrefix(c, typeClassPrefix) }

// classStyle is one semantic role: a mid-tone (Tailwind 400/500) fill with
// white text — the one palette that stays legible against both a white and a
// black canvas, so the viewer's day/night toggle only has to flip the page,
// never the diagram. Label drives the legend, so a color never appears in the
// viewer without the word that decodes it.
type classStyle struct {
	Fill   string
	Stroke string
	Label  string
	Bold   bool // run-status classes outline harder than the type classes
}

// classStyles are emitted only for the classes a diagram actually uses.
var classStyles = map[string]classStyle{
	// Definition view: the role each state plays.
	"wftask":    {"#64748b", "#334155", "Task", false},    // external: invokes a function
	"wfchoice":  {"#38bdf8", "#0369a1", "Choice", false},  // process: logic / decision
	"wfwait":    {"#94a3b8", "#475569", "Wait", false},    // standby: passive / waiting
	"wfsucceed": {"#10b981", "#047857", "Succeed", false}, // leader: terminal success
	"wffail":    {"#fb7185", "#be123c", "Fail", false},    // resource: terminal failure
	// Parallel/Map need no class: the container is structure (see renderMermaid).

	// Run view: what this run did. Unreached is the least saturated so visited
	// states carry the eye. These must not collide with the type colors that
	// survive into a run view (the routing-only states): Choice stays sky,
	// distinct from active's amber, while Succeed/Fail intentionally match ok
	// and failed — a Succeed state IS success.
	string(statusOK):        {"#10b981", "#047857", "succeeded", true},
	string(statusActive):    {"#f59e0b", "#b45309", "active", true},
	string(statusFailed):    {"#fb7185", "#be123c", "failed", true},
	string(statusUnreached): {"#94a3b8", "#475569", "unreached", false},
}

func (c classStyle) classDef() string {
	def := fmt.Sprintf("fill:%s,stroke:%s,color:#fff", c.Fill, c.Stroke)
	if c.Bold {
		def += ",stroke-width:2px"
	}
	return def
}

// renderMermaid renders spec as a stateDiagram-v2, and returns the classes it
// used so a caller can build a legend for exactly those (a color must never
// reach the viewer without the word that decodes it). With overlay non-nil
// every node is colored by its run status instead of its state type — in a run
// view the question is where the run got to, not what kind of state each node
// is (and it keeps a node from carrying two competing classes). Output is
// deterministic: states are emitted in sorted order.
func renderMermaid(spec fv1.WorkflowSpec, overlay map[string]nodeStatus) (string, []string) {
	var b strings.Builder
	b.WriteString("stateDiagram-v2\n")
	fmt.Fprintf(&b, "    [*] --> %s\n", mermaidID(spec.StartAt))

	names := make([]string, 0, len(spec.States))
	for name := range spec.States {
		names = append(names, name)
	}
	slices.Sort(names)

	// nodeType records every node the overlay may color, keyed by mermaid id.
	// Fan-out containers are deliberately absent: a region is structure — its
	// composite shape already says "this fans out", and its status is whatever
	// its branches show — so filling it would be redundant ink competing with
	// the branch nodes inside it (and mermaid draws a cluster's title on its own
	// themed bar, where a forced white label goes invisible on a light canvas).
	nodeType := map[string]fv1.WorkflowStateType{}
	var notes []string

	for _, name := range names {
		st := spec.States[name]
		id := mermaidID(name)
		if id != name {
			fmt.Fprintf(&b, "    state %q as %s\n", name, id)
		}
		if len(st.Branches) > 0 {
			renderRegions(&b, name, st, nodeType)
		} else {
			nodeType[id] = st.Type
		}
		writeTransitions(&b, "    ", id, func(n string) string { return mermaidID(n) },
			st.Next, st.Default, st.Choices, st.Catch, st.IsTerminal())
		if n := stateNote(id, st); n != "" {
			notes = append(notes, n)
		}
	}

	for _, n := range notes {
		b.WriteString(n + "\n")
	}

	// A node's class is a pure function of its type and the overlay, so it is
	// assigned once, after the body is written — no per-node bookkeeping in the
	// loop, and no second pass that discards and rebuilds it for a run view.
	byClass := map[string][]string{}
	for id, t := range nodeType {
		c := classFor(id, t, overlay)
		byClass[c] = append(byClass[c], id)
	}
	// Not `return b.String(), writeClasses(...)`: the return values evaluate
	// left to right, so b.String() would snapshot the builder before the
	// classes were written into it.
	classes := writeClasses(&b, byClass)
	return b.String(), classes
}

// classFor is the class a node carries: its state type in a definition view;
// its run status in a run view. A routing-only state (Choice/Succeed/Fail)
// resolves in the fold and emits no events, so the log says nothing about it
// either way — it keeps its type color rather than claim "unreached".
func classFor(id string, t fv1.WorkflowStateType, overlay map[string]nodeStatus) string {
	if overlay == nil {
		return typeClass(t)
	}
	if s, ok := overlay[id]; ok {
		return string(s)
	}
	if routingOnly(t) {
		return typeClass(t)
	}
	return string(statusUnreached)
}

// routingOnly reports whether a state type is resolved inside the engine's fold
// and so never appears in the run log: only Tasks (and a Wait's timer) emit
// events, so a Choice/Succeed/Fail is neither "reached" nor "unreached" as far
// as the history is concerned.
func routingOnly(t fv1.WorkflowStateType) bool {
	switch t {
	case fv1.WorkflowStateChoice, fv1.WorkflowStateSucceed, fv1.WorkflowStateFail:
		return true
	}
	return false
}

// writeTransitions emits the outgoing edges of one state — the same vocabulary
// (next, choice rules, default, typed catches, terminal) at both the top level
// and inside a branch, so the two can never drift. resolve maps a target state
// name to its node id at this level (bare id up top, branch-scoped inside).
func writeTransitions(b *strings.Builder, indent, id string, resolve func(string) string,
	next, def string, choices []fv1.WorkflowChoiceRule, catch []fv1.WorkflowCatchRoute, terminal bool) {
	if next != "" {
		fmt.Fprintf(b, "%s%s --> %s\n", indent, id, resolve(next))
	}
	for i, c := range choices {
		fmt.Fprintf(b, "%s%s --> %s : rule %d\n", indent, id, resolve(c.Next), i+1)
	}
	if def != "" {
		fmt.Fprintf(b, "%s%s --> %s : default\n", indent, id, resolve(def))
	}
	for _, c := range catch {
		fmt.Fprintf(b, "%s%s --> %s : %s\n", indent, id, resolve(c.Next), c.ErrorType)
	}
	if terminal {
		fmt.Fprintf(b, "%s%s --> [*]\n", indent, id)
	}
}

// renderRegions renders a fan-out state's branches as a composite state, one
// concurrent region ("--"-separated) per branch, recording each branch node's
// type for the overlay.
func renderRegions(b *strings.Builder, parent string, st fv1.WorkflowState, nodeType map[string]fv1.WorkflowStateType) {
	fmt.Fprintf(b, "    state %s {\n", mermaidID(parent))
	for i, br := range renderedBranches(st) {
		if i > 0 {
			b.WriteString("        --\n")
		}
		resolve := func(name string) string { return branchNodeID(parent, strconv.Itoa(i), name) }
		bnames := make([]string, 0, len(br.States))
		for n := range br.States {
			bnames = append(bnames, n)
		}
		slices.Sort(bnames)
		for _, bn := range bnames {
			fmt.Fprintf(b, "        state %q as %s\n", bn, resolve(bn))
		}
		fmt.Fprintf(b, "        [*] --> %s\n", resolve(br.StartAt))
		for _, bn := range bnames {
			bst := br.States[bn]
			bid := resolve(bn)
			writeTransitions(b, "        ", bid, resolve, bst.Next, bst.Default, bst.Choices, bst.Catch, bst.IsTerminal())
			nodeType[bid] = bst.Type
		}
	}
	b.WriteString("    }\n")
}

// renderedBranches is the branches the diagram draws. A Map's branch is a
// per-item TEMPLATE, not N concurrent machines, so it is drawn once as region
// mapTemplateBranch; the real fan-out width is data-driven and rides in the
// note. eventNodeID folds every item's events onto that same region.
func renderedBranches(st fv1.WorkflowState) []fv1.WorkflowBranch {
	if st.Type == fv1.WorkflowStateMap && len(st.Branches) > 1 {
		return st.Branches[:1]
	}
	return st.Branches
}

// stateNote surfaces the fields the graph shape cannot: a Map's fan-out source
// and bound, and a Wait's delay.
func stateNote(id string, st fv1.WorkflowState) string {
	switch st.Type {
	case fv1.WorkflowStateMap:
		note := "Map"
		if st.ItemsPath != "" {
			note += " over " + st.ItemsPath
		}
		if st.MaxConcurrency > 0 {
			note += fmt.Sprintf(" (maxConcurrency %d)", st.MaxConcurrency)
		}
		return fmt.Sprintf("    note right of %s : %s", id, note)
	case fv1.WorkflowStateWait:
		if st.Duration != nil {
			return fmt.Sprintf("    note right of %s : Wait %s", id, st.Duration.Duration)
		}
	}
	return ""
}

// writeClasses emits a classDef and its assignment for every class in use, in
// sorted order so the rendering stays deterministic, and returns those classes
// for the legend.
func writeClasses(b *strings.Builder, byClass map[string][]string) []string {
	classes := make([]string, 0, len(byClass))
	for c := range byClass {
		if _, known := classStyles[c]; known {
			classes = append(classes, c)
		}
	}
	slices.Sort(classes)
	for _, c := range classes {
		fmt.Fprintf(b, "    classDef %s %s\n", c, classStyles[c].classDef())
	}
	for _, c := range classes {
		ids := slices.Clone(byClass[c])
		slices.Sort(ids)
		fmt.Fprintf(b, "    class %s %s\n", strings.Join(ids, ","), c)
	}
	return classes
}
