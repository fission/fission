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

// branchNodeID scopes a branch state's id under its region. Branch state names
// are only unique within their branch — a top-level state (or another region)
// may reuse the name, and mermaid ids are global.
func branchNodeID(parent string, idx int, name string) string {
	return mermaidID(parent, strconv.Itoa(idx), name)
}

func typeClass(t fv1.WorkflowStateType) string {
	return "wf" + strings.ToLower(string(t))
}

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

// mermaidFromSpec renders the definition view of a workflow.
func mermaidFromSpec(spec fv1.WorkflowSpec) string {
	d, _ := renderMermaid(spec, nil)
	return d
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

	byClass := map[string][]string{}
	nodeType := map[string]fv1.WorkflowStateType{}
	var allNodes, notes []string

	for _, name := range names {
		st := spec.States[name]
		id := mermaidID(name)
		if id != name {
			fmt.Fprintf(&b, "    state %q as %s\n", name, id)
		}
		isRegion := len(st.Branches) > 0
		if isRegion {
			renderRegions(&b, name, st, byClass, &allNodes, nodeType)
		}
		if st.Next != "" {
			fmt.Fprintf(&b, "    %s --> %s\n", id, mermaidID(st.Next))
		}
		for i, c := range st.Choices {
			fmt.Fprintf(&b, "    %s --> %s : rule %d\n", id, mermaidID(c.Next), i+1)
		}
		if st.Default != "" {
			fmt.Fprintf(&b, "    %s --> %s : default\n", id, mermaidID(st.Default))
		}
		for _, c := range st.Catch {
			fmt.Fprintf(&b, "    %s --> %s : %s\n", id, mermaidID(c.Next), c.ErrorType)
		}
		if st.IsTerminal() {
			fmt.Fprintf(&b, "    %s --> [*]\n", id)
		}
		// A fan-out container is structure, not content: its composite shape
		// already says "this fans out", and its status is whatever its branches
		// show — filling it too is redundant ink competing with the branches
		// inside it. (Mermaid also draws a cluster's title on its own themed
		// bar, where a forced white label goes invisible on a light canvas.)
		// So it is left unclassed, and never enters the set the overlay colors.
		if !isRegion {
			byClass[typeClass(st.Type)] = append(byClass[typeClass(st.Type)], id)
			allNodes = append(allNodes, id)
			nodeType[id] = st.Type
		}
		if n := stateNote(id, st); n != "" {
			notes = append(notes, n)
		}
	}

	for _, n := range notes {
		b.WriteString(n + "\n")
	}
	if overlay != nil {
		byClass = map[string][]string{}
		for _, id := range allNodes {
			switch s, ok := overlay[id]; {
			case ok:
				byClass[string(s)] = append(byClass[string(s)], id)
			case routingOnly(nodeType[id]):
				// This state cannot produce step events, so the log says
				// nothing about it either way — keep its type color rather
				// than claim the run never reached it.
				byClass[typeClass(nodeType[id])] = append(byClass[typeClass(nodeType[id])], id)
			default:
				byClass[string(statusUnreached)] = append(byClass[string(statusUnreached)], id)
			}
		}
	}
	// Not `return b.String(), writeClasses(...)`: the return values evaluate
	// left to right, so b.String() would snapshot the builder before the
	// classes were written into it.
	classes := writeClasses(&b, byClass)
	return b.String(), classes
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

// renderRegions renders a fan-out state's branches as a composite state, one
// concurrent region ("--"-separated) per branch.
func renderRegions(b *strings.Builder, parent string, st fv1.WorkflowState, byClass map[string][]string, allNodes *[]string, nodeType map[string]fv1.WorkflowStateType) {
	branches := st.Branches
	// A Map's branch is a per-item TEMPLATE, not N concurrent machines: render
	// it once. The real fan-out width is data-driven and rides in the note.
	if st.Type == fv1.WorkflowStateMap && len(branches) > 1 {
		branches = branches[:1]
	}
	fmt.Fprintf(b, "    state %s {\n", mermaidID(parent))
	for i, br := range branches {
		if i > 0 {
			b.WriteString("        --\n")
		}
		bnames := make([]string, 0, len(br.States))
		for n := range br.States {
			bnames = append(bnames, n)
		}
		slices.Sort(bnames)
		for _, bn := range bnames {
			fmt.Fprintf(b, "        state %q as %s\n", bn, branchNodeID(parent, i, bn))
		}
		fmt.Fprintf(b, "        [*] --> %s\n", branchNodeID(parent, i, br.StartAt))
		for _, bn := range bnames {
			bst := br.States[bn]
			bid := branchNodeID(parent, i, bn)
			if bst.Next != "" {
				fmt.Fprintf(b, "        %s --> %s\n", bid, branchNodeID(parent, i, bst.Next))
			}
			for j, c := range bst.Choices {
				fmt.Fprintf(b, "        %s --> %s : rule %d\n", bid, branchNodeID(parent, i, c.Next), j+1)
			}
			if bst.Default != "" {
				fmt.Fprintf(b, "        %s --> %s : default\n", bid, branchNodeID(parent, i, bst.Default))
			}
			for _, c := range bst.Catch {
				fmt.Fprintf(b, "        %s --> %s : %s\n", bid, branchNodeID(parent, i, c.Next), c.ErrorType)
			}
			if bst.End || bst.Type == fv1.WorkflowStateSucceed || bst.Type == fv1.WorkflowStateFail {
				fmt.Fprintf(b, "        %s --> [*]\n", bid)
			}
			byClass[typeClass(bst.Type)] = append(byClass[typeClass(bst.Type)], bid)
			*allNodes = append(*allNodes, bid)
			nodeType[bid] = bst.Type
		}
	}
	b.WriteString("    }\n")
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
