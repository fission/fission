// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"fmt"
	"sort"
	"strings"
)

// Markdown renders a run as a GitHub-flavoured markdown report suitable for the
// step summary: a metadata header, a per-scenario metric table, and a breaches
// section.
func Markdown(run Run, breaches []Breach) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Fission benchmark — %s\n\n", run.RunID)
	if run.FissionVersion != "" {
		fmt.Fprintf(&b, "- Fission: `%s`\n", run.FissionVersion)
	}
	if run.K8sVersion != "" {
		fmt.Fprintf(&b, "- Kubernetes: `%s`\n", run.K8sVersion)
	}
	if run.GitSHA != "" {
		fmt.Fprintf(&b, "- Commit: `%s`\n", run.GitSHA)
	}
	fmt.Fprintf(&b, "- Duration: %s\n\n", run.FinishedAt.Sub(run.StartedAt).Round(1e9))

	if len(breaches) == 0 {
		b.WriteString("✅ All configured thresholds passed.\n\n")
	} else {
		fmt.Fprintf(&b, "❌ %d threshold breach(es):\n\n", len(breaches))
		for _, br := range breaches {
			if br.Kind == "missing" {
				fmt.Fprintf(&b, "- `%s/%s` missing from results\n", br.Scenario, br.Metric)
			} else {
				fmt.Fprintf(&b, "- %s\n", br.String())
			}
		}
		b.WriteString("\n")
	}

	for _, s := range run.Scenarios {
		fmt.Fprintf(&b, "## %s\n\n", s.Name)
		switch {
		case s.Skipped:
			fmt.Fprintf(&b, "_skipped: %s_\n\n", s.Skip)
			continue
		case s.Error != "":
			fmt.Fprintf(&b, "⚠️ error: %s\n\n", s.Error)
			continue
		}
		if len(s.Meta) > 0 {
			b.WriteString(metaLine(s.Meta) + "\n\n")
		}
		b.WriteString("| metric | value | unit |\n|---|---:|---|\n")
		for _, m := range s.Metrics {
			fmt.Fprintf(&b, "| %s | %.3f | %s |\n", m.Name, m.Value, m.Unit)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func metaLine(meta map[string]string) string {
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=`%s`", k, meta[k]))
	}
	return strings.Join(parts, ", ")
}
