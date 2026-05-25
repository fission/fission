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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/duration"
	"sigs.k8s.io/yaml"

	"github.com/fission/fission/pkg/conditions"
)

// NoneValue is rendered in a status column when a controller has not yet
// written the condition (e.g. a freshly created resource, or a resource
// whose controller does not emit that condition).
const NoneValue = "<none>"

// OutputFormat is the validated value of the -o/--output flag.
type OutputFormat string

const (
	OutputTable OutputFormat = ""     // default human table
	OutputWide  OutputFormat = "wide" // table + extra columns
	OutputJSON  OutputFormat = "json"
	OutputYAML  OutputFormat = "yaml"
)

// ParseOutputFormat validates the -o value (empty is allowed and means table).
func ParseOutputFormat(s string) (OutputFormat, error) {
	switch f := OutputFormat(strings.ToLower(s)); f {
	case OutputTable, OutputWide, OutputJSON, OutputYAML:
		return f, nil
	default:
		return "", fmt.Errorf("invalid output format %q: valid values are wide, json, yaml", s)
	}
}

// encode marshals v as JSON or YAML. It is the structured half of the printer,
// kept pure (no stdout) so it is straightforward to unit test.
func encode(format OutputFormat, v any) ([]byte, error) {
	switch format {
	case OutputJSON:
		return json.MarshalIndent(v, "", "  ")
	case OutputYAML:
		return yaml.Marshal(v)
	default:
		return nil, fmt.Errorf("encode called with non-structured format %q", format)
	}
}

// AgeOf renders an object's age from its creation timestamp, kubectl-style.
func AgeOf(t metav1.Time) string {
	if t.IsZero() {
		return NoneValue
	}
	return duration.HumanDuration(time.Since(t.Time))
}

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

// PrintItems renders a slice of typed items as a table: row maps each item to
// its cells, in the same order as headers. It is sugar over PrintTable that
// removes the build-a-[][]string loop repeated across the list commands.
func PrintItems[T any](headers []string, items []T, row func(T) []string) {
	rows := make([][]string, 0, len(items))
	for _, it := range items {
		rows = append(rows, row(it))
	}
	PrintTable(headers, rows)
}

// PrintObjects renders a slice of items in the requested format. For json/yaml
// it marshals items (a JSON array / YAML sequence). For table it uses
// headers+row; for wide it appends wideExtra columns (e.g. AGE) after the base
// columns. It is the single entry point for the list commands.
func PrintObjects[T any](format OutputFormat, items []T, headers []string, row func(T) []string, wideExtra []string, wideRow func(T) []string) error {
	switch format {
	case OutputJSON, OutputYAML:
		b, err := encode(format, items)
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	case OutputWide:
		hdr := append(append([]string{}, headers...), wideExtra...)
		PrintItems(hdr, items, func(t T) []string {
			return append(row(t), wideRow(t)...)
		})
		return nil
	default: // OutputTable
		PrintItems(headers, items, row)
		return nil
	}
}

// PrintStructured prints v as json/yaml and returns true when the format is
// structured; for table/wide it prints nothing and returns false so describe
// commands fall back to their own human rendering.
func PrintStructured(format OutputFormat, v any) (bool, error) {
	switch format {
	case OutputJSON, OutputYAML:
		b, err := encode(format, v)
		if err != nil {
			return true, err
		}
		fmt.Println(string(b))
		return true, nil
	default:
		return false, nil
	}
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
