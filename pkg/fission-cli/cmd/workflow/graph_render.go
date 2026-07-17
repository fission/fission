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

// classDefs are emitted only for the classes actually used, keeping the
// diagram tight. Colors read on both mermaid's light and dark themes.
var classDefFor = map[string]string{
	"wftask":     "fill:#1f3a5f,stroke:#4a90d9,color:#fff",
	"wfchoice":   "fill:#43305f,stroke:#a07ed9,color:#fff",
	"wfparallel": "fill:#1f4f4a,stroke:#4ad9c0,color:#fff",
	"wfmap":      "fill:#1f4f4a,stroke:#4ad9c0,color:#fff",
	"wfwait":     "fill:#5f4a1f,stroke:#d9a04a,color:#fff",
	"wfsucceed":  "fill:#1f5f2f,stroke:#4ad96a,color:#fff",
	"wffail":     "fill:#5f1f1f,stroke:#d94a4a,color:#fff",

	string(statusOK):        "fill:#1f5f2f,stroke:#4ad96a,color:#fff,stroke-width:2px",
	string(statusFailed):    "fill:#5f1f1f,stroke:#d94a4a,color:#fff,stroke-width:2px",
	string(statusActive):    "fill:#5f4a1f,stroke:#d9a04a,color:#fff,stroke-width:2px",
	string(statusUnreached): "fill:#2b2b2b,stroke:#555,color:#888",
}

// mermaidFromSpec renders the definition view of a workflow.
func mermaidFromSpec(spec fv1.WorkflowSpec) string { return renderMermaid(spec, nil) }

// renderMermaid renders spec as a stateDiagram-v2. With overlay non-nil every
// node is colored by its run status instead of its state type — in a run view
// the question is where the run got to, not what kind of state each node is
// (and it keeps a node from carrying two competing classes). Output is
// deterministic: states are emitted in sorted order.
func renderMermaid(spec fv1.WorkflowSpec, overlay map[string]nodeStatus) string {
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
		if len(st.Branches) > 0 {
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
		byClass[typeClass(st.Type)] = append(byClass[typeClass(st.Type)], id)
		allNodes = append(allNodes, id)
		nodeType[id] = st.Type
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
	writeClasses(&b, byClass)
	return b.String()
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
// sorted order so the rendering stays deterministic.
func writeClasses(b *strings.Builder, byClass map[string][]string) {
	classes := make([]string, 0, len(byClass))
	for c := range byClass {
		classes = append(classes, c)
	}
	slices.Sort(classes)
	for _, c := range classes {
		def, ok := classDefFor[c]
		if !ok {
			continue
		}
		fmt.Fprintf(b, "    classDef %s %s\n", c, def)
	}
	for _, c := range classes {
		ids := slices.Clone(byClass[c])
		slices.Sort(ids)
		fmt.Fprintf(b, "    class %s %s\n", strings.Join(ids, ","), c)
	}
}
