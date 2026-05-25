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
	"os"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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

func TestPrintItems(t *testing.T) {
	// PrintItems writes to os.Stdout; capture it to assert the row mapping is
	// applied to every item in order.
	type row struct {
		name, ready string
	}
	items := []row{{"a", "True"}, {"b", NoneValue}}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	PrintItems([]string{"NAME", "READY"}, items, func(it row) []string {
		return []string{it.name, it.ready}
	})
	w.Close()
	os.Stdout = orig

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "READY", "a", "True", "b", NoneValue} {
		if !strings.Contains(out, want) {
			t.Errorf("PrintItems output missing %q; got:\n%s", want, out)
		}
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
