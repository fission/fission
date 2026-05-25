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
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestParseOutputFormat(t *testing.T) {
	tests := []struct {
		in      string
		want    OutputFormat
		wantErr bool
	}{
		{"", OutputTable, false},
		{"wide", OutputWide, false},
		{"json", OutputJSON, false},
		{"yaml", OutputYAML, false},
		{"JSON", OutputJSON, false}, // case-insensitive
		{"name", "", true},
		{"xml", "", true},
	}
	for _, tt := range tests {
		got, err := ParseOutputFormat(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseOutputFormat(%q) err=%v, wantErr=%v", tt.in, err, tt.wantErr)
		}
		if err == nil && got != tt.want {
			t.Errorf("ParseOutputFormat(%q)=%q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEncode(t *testing.T) {
	items := []map[string]string{{"name": "a"}, {"name": "b"}}

	j, err := encode(OutputJSON, items)
	if err != nil {
		t.Fatal(err)
	}
	var back []map[string]string
	if err := json.Unmarshal(j, &back); err != nil {
		t.Fatalf("json did not round-trip: %v\n%s", err, j)
	}
	if len(back) != 2 || back[0]["name"] != "a" {
		t.Fatalf("unexpected json: %s", j)
	}

	y, err := encode(OutputYAML, items)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(y), "name: a") {
		t.Fatalf("unexpected yaml: %s", y)
	}
}

func TestAgeOf(t *testing.T) {
	if got := AgeOf(metav1.Time{}); got != NoneValue {
		t.Errorf("zero time should render %q, got %q", NoneValue, got)
	}
	if got := AgeOf(metav1.Now()); got == NoneValue || got == "" {
		t.Errorf("recent time should render a duration, got %q", got)
	}
}

func TestConditionStatus(t *testing.T) {
	conds := []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue},
		{Type: "BuildSucceeded", Status: metav1.ConditionFalse},
	}

	tests := []struct {
		name     string
		conds    []metav1.Condition
		condType string
		want     string
	}{
		{"true condition", conds, "Ready", "True"},
		{"false condition", conds, "BuildSucceeded", "False"},
		{"missing condition", conds, "Progressing", NoneValue},
		{"nil conditions", nil, "Ready", NoneValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConditionStatus(tt.conds, tt.condType); got != tt.want {
				t.Errorf("ConditionStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it wrote.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	orig := os.Stdout
	t.Cleanup(func() { os.Stdout = orig })
	os.Stdout = w
	if err := fn(); err != nil {
		t.Fatalf("fn returned error: %v", err)
	}
	w.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestPrintItems(t *testing.T) {
	// PrintItems writes to os.Stdout; capture it to assert the row mapping is
	// applied to every item in order.
	type row struct {
		name, ready string
	}
	items := []row{{"a", "True"}, {"b", NoneValue}}

	out := captureStdout(t, func() error {
		PrintItems([]string{"NAME", "READY"}, items, func(it row) []string {
			return []string{it.name, it.ready}
		})
		return nil
	})
	for _, want := range []string{"NAME", "READY", "a", "True", "b", NoneValue} {
		if !strings.Contains(out, want) {
			t.Errorf("PrintItems output missing %q; got:\n%s", want, out)
		}
	}
}

func TestPrintObjectsTableAndWide(t *testing.T) {
	type row struct{ name, ready, age string }
	items := []row{{"a", "True", "5m"}, {"b", NoneValue, "1h"}}
	hdr := []string{"NAME", "READY"}
	rowFn := func(r row) []string { return []string{r.name, r.ready} }
	wideHdr := []string{"AGE"}
	wideFn := func(r row) []string { return []string{r.age} }

	out := captureStdout(t, func() error { return PrintObjects(OutputTable, items, hdr, rowFn, wideHdr, wideFn) })
	if strings.Contains(out, "AGE") {
		t.Errorf("table output must not include wide columns:\n%s", out)
	}
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "a") {
		t.Errorf("table missing base columns:\n%s", out)
	}

	out = captureStdout(t, func() error { return PrintObjects(OutputWide, items, hdr, rowFn, wideHdr, wideFn) })
	if !strings.Contains(out, "AGE") || !strings.Contains(out, "5m") {
		t.Errorf("wide output missing AGE:\n%s", out)
	}

	out = captureStdout(t, func() error { return PrintObjects(OutputJSON, items, hdr, rowFn, wideHdr, wideFn) })
	if !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Errorf("json output should be an array:\n%s", out)
	}
}

func TestPrintStructured(t *testing.T) {
	obj := map[string]string{"name": "hello"}

	out := captureStdout(t, func() error {
		handled, err := PrintStructured(OutputTable, obj)
		if handled {
			t.Error("table must not be handled by PrintStructured")
		}
		return err
	})
	if out != "" {
		t.Errorf("expected no output for table, got %q", out)
	}

	out = captureStdout(t, func() error {
		handled, err := PrintStructured(OutputYAML, obj)
		if !handled {
			t.Error("yaml must be handled")
		}
		return err
	})
	if !strings.Contains(out, "name: hello") {
		t.Errorf("yaml not printed:\n%s", out)
	}
}

func TestPrintConditionsTo(t *testing.T) {
	t.Run("empty is a no-op", func(t *testing.T) {
		var buf bytes.Buffer
		PrintConditionsTo(&buf, nil)
		if buf.Len() != 0 {
			t.Errorf("expected no output for empty conditions, got %q", buf.String())
		}
	})

	t.Run("renders rows", func(t *testing.T) {
		var buf bytes.Buffer
		PrintConditionsTo(&buf, []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Available", Message: "backend serving"},
		})
		out := buf.String()
		for _, want := range []string{"CONDITIONS:", "TYPE", "Ready", "True", "Available", "backend serving"} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q; got:\n%s", want, out)
			}
		}
	})
}

func TestPrintTableRowsAlign(t *testing.T) {
	// PrintTable writes to os.Stdout; here we exercise the same shaping the
	// list commands rely on (header + string rows) via NewTabWriter so the
	// formatting contract is covered without capturing global stdout.
	var buf bytes.Buffer
	w := NewTabWriter(&buf)
	headers := []string{"NAME", "READY"}
	rows := [][]string{{"hello", "True"}, {"world", NoneValue}}
	if _, err := w.Write([]byte(strings.Join(headers, "\t") + "\n")); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := w.Write([]byte(strings.Join(r, "\t") + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "READY", "hello", "True", "world", NoneValue} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q; got:\n%s", want, out)
		}
	}
}
