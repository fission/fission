// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/fission/fission/test/benchmark/pkg/harness"
	"github.com/fission/fission/test/benchmark/pkg/report"
	"github.com/fission/fission/test/benchmark/pkg/scenario"
)

func runCmd() *cobra.Command {
	var (
		scenariosCSV, tagsCSV                       string
		kubeconfig, fissionNS, workNS               string
		routerURL, routerInternalURL, prometheusURL string
		pprofCSV, artifactDir, configPath, outPath  string
		runID, fissionVersion, k8sVersion, gitSHA   string
		duration, warmup                            time.Duration
		concurrency, coldIterations, poolsize       int
		repetitions                                 int
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run benchmark scenarios against a cluster and write results JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			params, err := loadParams(configPath)
			if err != nil {
				return err
			}
			// Flag overrides take precedence over the config file.
			if duration > 0 {
				params.WarmDuration = scenario.Duration(duration)
			}
			if warmup > 0 {
				params.WarmWarmup = scenario.Duration(warmup)
			}
			if concurrency > 0 {
				params.WarmConcurrency = concurrency
			}
			if coldIterations > 0 {
				params.ColdIterations = coldIterations
			}
			if poolsize > 0 {
				params.Poolsize = poolsize
			}
			if repetitions > 0 {
				params.Repetitions = repetitions
			}

			selected := scenario.Select(scenario.BuildAll(params), splitCSV(scenariosCSV), splitCSV(tagsCSV))
			if len(selected) == 0 {
				return fmt.Errorf("no scenarios selected")
			}
			if runID == "" {
				runID = fmt.Sprintf("run-%d", time.Now().Unix())
			}

			env, err := harness.New(ctx, harness.Config{
				Kubeconfig:        kubeconfig,
				FissionNamespace:  fissionNS,
				WorkNamespace:     workNS,
				RouterURL:         routerURL,
				RouterInternalURL: routerInternalURL,
				PrometheusURL:     prometheusURL,
				PprofTargets:      parsePprofTargets(pprofCSV),
				ArtifactDir:       artifactDir,
				RunID:             runID,
			})
			if err != nil {
				return err
			}
			if k8sVersion == "" {
				k8sVersion = env.Clients.ServerVersion()
			}

			fmt.Fprintf(os.Stderr, "running %d scenario(s): %v\n", len(selected), scenario.Names(selected))
			run := scenario.Run(ctx, env, selected, params.Repetitions)
			run.FissionVersion = fissionVersion
			run.K8sVersion = k8sVersion
			run.GitSHA = gitSHA

			if outPath == "" {
				outPath = "results.json"
			}
			if err := report.WriteRun(outPath, run); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "wrote %s\n", outPath)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&scenariosCSV, "scenarios", "", "comma-separated scenario names (default: all)")
	f.StringVar(&tagsCSV, "tags", "", "comma-separated tags to select (e.g. smoke,latency)")
	f.StringVar(&kubeconfig, "kubeconfig", "", "path to kubeconfig (default: KUBECONFIG/in-cluster)")
	f.StringVar(&fissionNS, "fission-namespace", "fission", "Fission control-plane namespace")
	f.StringVar(&workNS, "namespace", "default", "namespace for benchmark resources")
	f.StringVar(&routerURL, "router-url", "http://127.0.0.1:8888", "public router base URL")
	f.StringVar(&routerInternalURL, "router-internal-url", "http://127.0.0.1:8889", "router internal listener base URL")
	f.StringVar(&prometheusURL, "prometheus-url", "", "Prometheus base URL for server-side capture (optional)")
	f.StringVar(&pprofCSV, "pprof", "", "pprof targets, e.g. router=http://127.0.0.1:6060,executor=http://127.0.0.1:6061")
	f.StringVar(&artifactDir, "artifact-dir", "", "directory for pprof/range-query dumps (optional)")
	f.StringVar(&configPath, "config", "", "scenario params YAML (optional)")
	f.StringVar(&outPath, "out", "results.json", "results JSON output path")
	f.StringVar(&runID, "run-id", "", "run identifier (default: run-<unix>)")
	f.StringVar(&fissionVersion, "fission-version", "", "Fission version label for the results")
	f.StringVar(&k8sVersion, "k8s-version", "", "Kubernetes version label (default: discovered)")
	f.StringVar(&gitSHA, "git-sha", "", "git commit label for the results")
	f.DurationVar(&duration, "duration", 0, "measured window for load scenarios (overrides config)")
	f.DurationVar(&warmup, "warmup", 0, "warm-up window discarded before measuring (overrides config)")
	f.IntVar(&concurrency, "concurrency", 0, "warm-path concurrency (overrides config)")
	f.IntVar(&coldIterations, "cold-iterations", 0, "cold-start iterations (overrides config)")
	f.IntVar(&repetitions, "repetitions", 0, "re-run each scenario N times and report per-metric medians (overrides config)")
	f.IntVar(&poolsize, "poolsize", 0, "environment pool size (overrides config)")
	return cmd
}
