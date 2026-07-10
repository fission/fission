// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Command fission-benchmark runs end-to-end performance benchmarks against a
// Fission installation (any cluster, via kubeconfig) and reports/gates/compares
// the results. It is the portable front-end to the test/benchmark engine.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/fission/fission/test/benchmark/pkg/scenario"
)

func main() {
	// Cancel the command context on SIGINT/SIGTERM so a Ctrl-C cancels in-flight
	// scenarios and lets their deferred cleanup run instead of orphaning
	// benchmark resources on the cluster.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := rootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "fission-benchmark",
		Short:         "End-to-end performance benchmarks for a Fission installation",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(runCmd(), listCmd(), reportCmd(), compareCmd())
	return root
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available scenarios and their tags",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			for _, s := range scenario.BuildAll(scenario.DefaultParams()) {
				fmt.Fprintf(cmd.OutOrStdout(), "%-26s %s\n", s.Name(), strings.Join(s.Tags(), ","))
			}
			return nil
		},
	}
}

// splitCSV splits a comma-separated flag value into trimmed, non-empty items.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parsePprofTargets parses "router=http://127.0.0.1:6060,executor=http://..."
// into a name->URL map.
func parsePprofTargets(s string) map[string]string {
	items := splitCSV(s)
	if len(items) == 0 {
		return nil
	}
	m := make(map[string]string, len(items))
	for _, it := range items {
		if name, url, ok := strings.Cut(it, "="); ok {
			m[strings.TrimSpace(name)] = strings.TrimSpace(url)
		}
	}
	return m
}
