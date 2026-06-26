// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/fission/fission/test/benchmark/pkg/report"
)

func compareCmd() *cobra.Command {
	var (
		basePath, headPath string
		failPct            float64
		failOnRegression   bool
	)

	cmd := &cobra.Command{
		Use:   "compare",
		Short: "Compare two results files (e.g. version-vs-version or gates on/off)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			base, err := report.ReadRun(basePath)
			if err != nil {
				return err
			}
			head, err := report.ReadRun(headPath)
			if err != nil {
				return err
			}

			deltas := report.Compare(base, head)
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "SCENARIO/METRIC\tBASE\tHEAD\tΔ%\tREGRESSION")
			regressions := 0
			for _, d := range deltas {
				flag := ""
				// When the baseline is 0, percent change is undefined — flag any
				// regressing move (e.g. error_rate 0 -> 0.5) instead of letting
				// the 0% gate hide it.
				worse := d.Regression && (d.Base == 0 || abs(d.PctChange) >= failPct)
				if worse {
					flag = "yes"
					regressions++
				}
				fmt.Fprintf(w, "%s/%s\t%.3f\t%.3f\t%+.1f\t%s\n", d.Scenario, d.Metric, d.Base, d.Head, d.PctChange, flag)
			}
			if err := w.Flush(); err != nil {
				return err
			}

			if regressions > 0 && failOnRegression {
				return fmt.Errorf("%d regression(s) beyond %.1f%%", regressions, failPct)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&basePath, "base", "", "baseline results JSON (required)")
	f.StringVar(&headPath, "head", "", "candidate results JSON (required)")
	f.Float64Var(&failPct, "fail-pct", 10, "percent change that counts as a regression")
	f.BoolVar(&failOnRegression, "fail-on-regression", false, "exit non-zero on any flagged regression")
	_ = cmd.MarkFlagRequired("base")
	_ = cmd.MarkFlagRequired("head")
	return cmd
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
