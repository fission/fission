/*
Copyright 2026 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package util

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/fission/fission/pkg/conditions"
)

// NoneValue is rendered in a status column when a controller has not yet
// written the condition (e.g. a freshly created resource, or a resource
// whose controller does not emit that condition).
const NoneValue = "<none>"

// NewTabWriter returns a tabwriter configured with the Fission CLI's standard
// list/get column formatting. It centralises the
// tabwriter.NewWriter(w, 0, 0, 1, ' ', 0) literal that was duplicated across
// every list and get command.
func NewTabWriter(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 0, 1, ' ', 0)
}

// PrintTable writes a tab-separated header row followed by data rows to stdout
// using NewTabWriter, flushing before it returns. Callers build rows as
// [][]string; fmt.Sprintf("%v", x) at the call site preserves the existing
// rendering of ints, resource.Quantity, []string, etc.
func PrintTable(headers []string, rows [][]string) {
	w := NewTabWriter(os.Stdout)
	fmt.Fprintln(w, strings.Join(headers, "\t"))
	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	w.Flush()
}

// ConditionStatus renders the Status of the named condition for a status
// column: "True", "False" or "Unknown", or NoneValue when the controller has
// not written that condition yet.
func ConditionStatus(conds []metav1.Condition, conditionType string) string {
	c := conditions.Find(conds, conditionType)
	if c == nil {
		return NoneValue
	}
	return string(c.Status)
}

// PrintConditions writes a CONDITIONS table to stdout for describe-style
// commands. It is a no-op when conds is empty, so callers can invoke it
// unconditionally.
func PrintConditions(conds []metav1.Condition) {
	PrintConditionsTo(os.Stdout, conds)
}

// PrintConditionsTo writes a CONDITIONS table (TYPE / STATUS / REASON /
// MESSAGE / LASTTRANSITION) to w. It is a no-op when conds is empty.
func PrintConditionsTo(out io.Writer, conds []metav1.Condition) {
	if len(conds) == 0 {
		return
	}
	w := NewTabWriter(out)
	fmt.Fprintln(w, "\nCONDITIONS:")
	fmt.Fprintln(w, "TYPE\tSTATUS\tREASON\tMESSAGE\tLASTTRANSITION")
	for _, c := range conds {
		age := NoneValue
		if !c.LastTransitionTime.IsZero() {
			age = duration.HumanDuration(time.Since(c.LastTransitionTime.Time))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.Type, c.Status, c.Reason, c.Message, age)
	}
	w.Flush()
}
