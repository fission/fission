// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/fission/fission/test/benchmark/pkg/report"
)

func reportCmd() *cobra.Command {
	var (
		inPath, thresholdsPath, summaryPath string
		trendSmaller, trendBigger           string
		failOnBreach                        bool
	)

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Render a summary, evaluate thresholds, and emit trend data from a results file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			run, err := report.ReadRun(inPath)
			if err != nil {
				return err
			}

			var breaches []report.Breach
			if thresholdsPath != "" {
				th, err := report.LoadThresholds(thresholdsPath)
				if err != nil {
					return err
				}
				breaches = th.Evaluate(run)
			}

			md := report.Markdown(run, breaches)
			if summaryPath != "" {
				if err := os.WriteFile(summaryPath, []byte(md), 0o644); err != nil {
					return err
				}
			} else {
				fmt.Fprint(cmd.OutOrStdout(), md)
			}

			if trendSmaller != "" || trendBigger != "" {
				if err := report.WriteTrend(run, trendSmaller, trendBigger); err != nil {
					return err
				}
			}

			// An errored scenario means the benchmark could not run it (not a
			// numeric threshold miss). Most scenarios carry no thresholds, so
			// without this they would pass the gate green on a broken cluster.
			var errored []string
			for _, s := range run.Scenarios {
				if s.Error != "" {
					errored = append(errored, s.Name)
				}
			}

			for _, b := range breaches {
				fmt.Fprintln(os.Stderr, "BREACH:", b.String())
			}
			for _, name := range errored {
				fmt.Fprintln(os.Stderr, "ERRORED:", name)
			}
			if failOnBreach && (len(breaches) > 0 || len(errored) > 0) {
				return fmt.Errorf("%d threshold breach(es), %d errored scenario(s)", len(breaches), len(errored))
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&inPath, "in", "results.json", "results JSON input path")
	f.StringVar(&thresholdsPath, "thresholds", "", "thresholds YAML (optional; enables gating)")
	f.StringVar(&summaryPath, "summary", "", "write markdown summary here (default: stdout)")
	f.StringVar(&trendSmaller, "trend-smaller", "", "write lower-is-better trend JSON here (optional)")
	f.StringVar(&trendBigger, "trend-bigger", "", "write higher-is-better trend JSON here (optional)")
	f.BoolVar(&failOnBreach, "fail-on-breach", true, "exit non-zero when a threshold is breached")
	return cmd
}
