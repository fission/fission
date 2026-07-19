// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"text/tabwriter"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	storagesvcClient "github.com/fission/fission/pkg/storagesvc/client"
	"github.com/fission/fission/pkg/svcinfo"
)

// historyEvent mirrors the workflow head's HistoryEvent wire shape (the CLI
// deliberately decodes loosely — a newer head may add fields).
type historyEvent struct {
	Seq     int64  `json:"seq"`
	At      string `json:"at"`
	Type    string `json:"type"`
	State   string `json:"state,omitempty"`
	Attempt int32  `json:"attempt,omitempty"`
	// Branch (the branch key) and Region ("state@entrySeq", naming the
	// fan-out instance) place a parallel-region step event; `runs graph`
	// needs both to color the right branch node.
	Branch string `json:"branch,omitempty"`
	Region string `json:"region,omitempty"`
	// Spec is set on RunStarted only: the snapshot this run executes for its
	// whole life. `runs graph` draws the history against it rather than the
	// live Workflow, which may have since been edited or deleted.
	Spec      *fv1.WorkflowSpec `json:"spec,omitempty"`
	ErrorType string            `json:"errorType,omitempty"`
	Cause     json.RawMessage   `json:"cause,omitempty"`
	Output    json.RawMessage   `json:"output,omitempty"`
	OutputRef string            `json:"outputRef,omitempty"`
}

type HistorySubCommand struct {
	cmd.CommandActioner
}

// History renders a run's full event log, read from the workflow head over
// the portless port-forward plane (CRDs deliberately do not hold history).
func History(input cli.Input) error {
	return (&HistorySubCommand{}).do(input)
}

func (opts *HistorySubCommand) do(input cli.Input) error {
	events, _, err := fetchHistory(input, &opts.CommandActioner)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "SEQ\tAT\tEVENT\tSTATE\tATTEMPT\tDETAIL\n")
	for _, e := range events {
		detail := e.ErrorType
		if input.Bool(flagkey.WfIO) && len(e.Output) > 0 {
			detail = string(e.Output)
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\n",
			e.Seq, e.At, e.Type, orDash(e.State), attemptOrDash(e.Attempt), orDash(detail))
	}
	return w.Flush()
}

// fetchHistory resolves the run, port-forwards to the workflow head, and
// reads the signed history endpoint.
func fetchHistory(input cli.Input, opts *cmd.CommandActioner) ([]historyEvent, *fv1.WorkflowRun, error) {
	runName := input.String(flagkey.WfName)
	if runName == "" {
		return nil, nil, errors.New("need a workflow run, use --name")
	}
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return nil, nil, fmt.Errorf("error resolving namespace: %w", err)
	}

	run, err := opts.Client().FissionClientSet.CoreV1().WorkflowRuns(namespace).Get(input.Context(), runName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("error getting workflow run: %w", err)
	}

	base, err := workflowBaseURL(input, opts)
	if err != nil {
		return nil, nil, err
	}

	q := url.Values{"uid": {string(run.UID)}}
	if input.Bool(flagkey.WfIO) {
		q.Set("io", "true")
	}
	histURL := fmt.Sprintf("%s/history/%s/%s?%s", base, namespace, runName, q.Encode())

	req, err := http.NewRequestWithContext(input.Context(), http.MethodGet, histURL, nil)
	if err != nil {
		return nil, nil, err
	}

	client := &http.Client{Timeout: 30 * time.Second, Transport: workflowTransport(input, opts)}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("reaching the workflow head: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("history endpoint: %s: %s", resp.Status, string(body))
	}

	var events []historyEvent
	if err := json.Unmarshal(body, &events); err != nil {
		return nil, nil, fmt.Errorf("decoding history: %w", err)
	}
	return events, run, nil
}

// workflowBaseURL resolves the workflow head over the portless plane;
// FISSION_WORKFLOW_URL overrides for hand-managed forwards and tests.
func workflowBaseURL(input cli.Input, opts *cmd.CommandActioner) (string, error) {
	if u := os.Getenv("FISSION_WORKFLOW_URL"); u != "" {
		return u, nil
	}
	localPort, err := util.SetupPortForwardToPort(input.Context(), opts.Client(),
		fissionNamespace(), "svc="+svcinfo.SvcWorkflow, svcinfo.PortWorkflow)
	if err != nil {
		return "", fmt.Errorf("port-forwarding to the workflow head (is workflows.enabled set?): %w", err)
	}
	return "http://127.0.0.1:" + localPort, nil
}

// workflowTransport signs requests with the ServiceWorkflow key when the
// cluster runs internal auth (empty secret = pass-through, matching the
// verifier). A FAILED secret read is not the same as "auth not configured":
// say so, or the resulting 401 is undebuggable.
func workflowTransport(input cli.Input, opts *cmd.CommandActioner) http.RoundTripper {
	master, err := storagesvcClient.HMACSecretFromCluster(input.Context(), opts.Client().KubernetesClient, fissionNamespace())
	if err != nil {
		console.Warn(fmt.Sprintf("could not read the internal auth secret (%v); sending unsigned requests — a 401 below means your kubeconfig lacks access to it, not that auth is off", err))
		return http.DefaultTransport
	}
	if len(master) == 0 {
		return http.DefaultTransport
	}
	return hmacauth.ServiceSigner(master, hmacauth.ServiceWorkflow, http.DefaultTransport, time.Now)
}

// fissionNamespace is where the Fission control plane lives; the env var
// wins, defaulting to "fission" (GetFissionNamespace returns "" when unset,
// which as a namespace argument silently queries nothing).
func fissionNamespace() string {
	if ns := util.GetFissionNamespace(); ns != "" {
		return ns
	}
	return "fission"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func attemptOrDash(a int32) string {
	if a == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", a)
}
