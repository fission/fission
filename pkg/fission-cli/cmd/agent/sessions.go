// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
	"github.com/fission/fission/pkg/fission-cli/flag"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/svcinfo"
)

// SessionCommands builds the `fission agent sessions` sub-group: read-only
// introspection over the agentruntime session registry (slice 4). It talks
// to the agentruntime's RegistryAPI (pkg/agentruntime/registry_api.go)
// directly over HTTP rather than the Fission clientset, exactly like
// `fission workflow history` talks to the workflow head — there is no CRD
// for a session.
func SessionCommands() *cobra.Command {
	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "list",
		Short: "List an agent function's sessions",
	}, SessionsList, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{flag.Namespace, flag.Output, flag.AgentToken},
	})
	getCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "get",
		Short: "Get one agent session",
	}, SessionsGet, flag.FlagSet{
		Required: []flag.Flag{flag.FnName, flag.AgentSession},
		Optional: []flag.Flag{flag.Namespace, flag.Output, flag.AgentToken},
	})

	command := &cobra.Command{
		Use:   "sessions",
		Short: "Inspect agent sessions (agentruntime registry)",
	}
	command.AddCommand(listCmd, getCmd)
	return command
}

// sessionStats mirrors agentruntime.SessionStats's wire shape
// (pkg/agentruntime/session.go); Tokens/Cost are always 0 today (reserved
// for the gateway integration) but decoded anyway so a newer server's values
// show up without a CLI change.
type sessionStats struct {
	Turns         int64 `json:"turns"`
	Errors        int64 `json:"errors"`
	ToolCalls     int64 `json:"toolCalls"`
	ActiveMillis  int64 `json:"activeMillis"`
	TokensIn      int64 `json:"tokensIn"`
	TokensOut     int64 `json:"tokensOut"`
	CostMicroUSD  int64 `json:"costMicroUSD"`
	Continuations int64 `json:"continuations"`
}

// sessionRecord mirrors agentruntime.SessionRecord's wire shape. The CLI
// decodes its own copy rather than importing pkg/agentruntime, the same
// reasoning history.go gives for historyEvent: the server package pulls in
// the statestore/MCP dependency graph that a CLI binary has no business
// carrying, and a wire mirror decoded loosely tolerates a newer server
// adding fields.
type sessionRecord struct {
	ID           string       `json:"id"`
	Agent        string       `json:"agent"`
	Namespace    string       `json:"namespace"`
	Status       string       `json:"status"`
	CreatedAt    time.Time    `json:"createdAt"`
	LastActiveAt time.Time    `json:"lastActiveAt"`
	CurrentPod   string       `json:"currentPod,omitempty"`
	Stats        sessionStats `json:"stats"`
}

// sessionsResponse mirrors ListSessions's response envelope
// (pkg/agentruntime/registry_api.go sessionsResponse). Next (a pagination
// cursor) is not surfaced by this CLI yet — `sessions list` always fetches a
// single page at the server's default/max limit.
type sessionsResponse struct {
	Sessions []sessionRecord `json:"sessions"`
	Next     string          `json:"next,omitempty"`
}

type SessionsListSubCommand struct {
	cmd.CommandActioner
}

type SessionsGetSubCommand struct {
	cmd.CommandActioner
}

func SessionsList(input cli.Input) error { return (&SessionsListSubCommand{}).do(input) }
func SessionsGet(input cli.Input) error  { return (&SessionsGetSubCommand{}).do(input) }

func (opts *SessionsListSubCommand) do(input cli.Input) error {
	agentName := input.String(flagkey.FnName)
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error resolving namespace: %w", err)
	}

	base, err := agentRuntimeBaseURL(input, &opts.CommandActioner)
	if err != nil {
		return err
	}
	reqURL := fmt.Sprintf("%s/registry/agents/%s/%s/sessions", base, url.PathEscape(namespace), url.PathEscape(agentName))

	body, err := agentRuntimeGet(input, reqURL)
	if err != nil {
		return sessionsFriendlyError(err, namespace, agentName)
	}

	var resp sessionsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("decoding sessions response: %w", err)
	}
	if resp.Next != "" {
		// This CLI fetches a single page (the server's default/max limit);
		// it does not yet follow Next, so a busy agent's session list here
		// is a truncated prefix, not the whole set.
		console.Warn("more sessions exist beyond this page; sessions list does not yet page through them")
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	headers := []string{"ID", "STATUS", "TURNS", "CONTINUATIONS", "LAST-ACTIVE", "POD"}
	row := func(s sessionRecord) []string {
		return []string{
			s.ID,
			s.Status,
			strconv.FormatInt(s.Stats.Turns, 10),
			strconv.FormatInt(s.Stats.Continuations, 10),
			timeOrDash(s.LastActiveAt),
			stringOrDash(s.CurrentPod),
		}
	}
	wideExtra := []string{"CREATED"}
	wideRow := func(s sessionRecord) []string { return []string{timeOrDash(s.CreatedAt)} }

	return util.PrintObjects(format, resp.Sessions, headers, row, wideExtra, wideRow)
}

func (opts *SessionsGetSubCommand) do(input cli.Input) error {
	agentName := input.String(flagkey.FnName)
	sessionID := input.String(flagkey.AgentSession)
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error resolving namespace: %w", err)
	}

	base, err := agentRuntimeBaseURL(input, &opts.CommandActioner)
	if err != nil {
		return err
	}
	reqURL := fmt.Sprintf("%s/registry/agents/%s/%s/sessions/%s", base,
		url.PathEscape(namespace), url.PathEscape(agentName), url.PathEscape(sessionID))

	body, err := agentRuntimeGet(input, reqURL)
	if err != nil {
		return sessionsFriendlyError(err, namespace, agentName)
	}

	var rec sessionRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return fmt.Errorf("decoding session response: %w", err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}
	if handled, err := util.PrintStructured(format, rec); err != nil || handled {
		return err
	}

	w := util.NewTabWriter(os.Stdout)
	fmt.Fprintf(w, "ID:\t%s\n", rec.ID)
	fmt.Fprintf(w, "AGENT:\t%s\n", rec.Agent)
	fmt.Fprintf(w, "NAMESPACE:\t%s\n", rec.Namespace)
	fmt.Fprintf(w, "STATUS:\t%s\n", rec.Status)
	fmt.Fprintf(w, "CREATED:\t%s\n", timeOrDash(rec.CreatedAt))
	fmt.Fprintf(w, "LAST-ACTIVE:\t%s\n", timeOrDash(rec.LastActiveAt))
	fmt.Fprintf(w, "POD:\t%s\n", stringOrDash(rec.CurrentPod))
	fmt.Fprintf(w, "TURNS:\t%d\n", rec.Stats.Turns)
	fmt.Fprintf(w, "ERRORS:\t%d\n", rec.Stats.Errors)
	fmt.Fprintf(w, "TOOL-CALLS:\t%d\n", rec.Stats.ToolCalls)
	fmt.Fprintf(w, "ACTIVE-MILLIS:\t%d\n", rec.Stats.ActiveMillis)
	fmt.Fprintf(w, "TOKENS-IN:\t%d\n", rec.Stats.TokensIn)
	fmt.Fprintf(w, "TOKENS-OUT:\t%d\n", rec.Stats.TokensOut)
	fmt.Fprintf(w, "COST-MICRO-USD:\t%d\n", rec.Stats.CostMicroUSD)
	fmt.Fprintf(w, "CONTINUATIONS:\t%d\n", rec.Stats.Continuations)
	return w.Flush()
}

// agentRuntimeError wraps a non-2xx response from the agent runtime with its
// status and the server's {"error": "..."} message (dispatcher.go/
// registry_api.go's writeErr shape), so sessionsFriendlyError can map
// specific codes without re-parsing the body.
type agentRuntimeError struct {
	status  int
	message string
}

func (e *agentRuntimeError) Error() string {
	return fmt.Sprintf("agent runtime returned %s: %s", http.StatusText(e.status), e.message)
}

// sessionsFriendlyError maps the two errors an operator actually needs to
// tell apart at a glance — "wrong agent/namespace" vs "wrong token scope" —
// to a message naming the likely cause; every other error (a network
// failure, a decode error) passes through unchanged.
func sessionsFriendlyError(err error, namespace, agentName string) error {
	var ae *agentRuntimeError
	if !errors.As(err, &ae) {
		return err
	}
	switch ae.status {
	case http.StatusNotFound:
		return fmt.Errorf("agent not found or has no session data: %s/%s (%s)", namespace, agentName, ae.message)
	case http.StatusForbidden:
		return fmt.Errorf("token not authorized for namespace %s (%s)", namespace, ae.message)
	default:
		return err
	}
}

// agentRuntimeGet issues a bearer-authenticated or anonymous GET against the
// agent runtime and returns the raw response body, or an *agentRuntimeError
// for a non-200 response.
func agentRuntimeGet(input cli.Input, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(input.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if token := agentToken(input); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching the agent runtime: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &agentRuntimeError{status: resp.StatusCode, message: errorMessage(body)}
	}
	return body, nil
}

// errorMessage extracts the "error" field from a {"error": "..."} body
// (writeErr's shape); anything else is reported as-is, trimmed.
func errorMessage(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(body))
}

// agentToken resolves the bearer token for an agent runtime request:
// --agent-token wins, then FISSION_AGENT_TOKEN; an empty result sends an
// unauthenticated request, which the server accepts only when
// agentRuntime.allowInsecure is set.
func agentToken(input cli.Input) string {
	if t := input.String(flagkey.AgentToken); t != "" {
		return t
	}
	return os.Getenv("FISSION_AGENT_TOKEN")
}

// agentRuntimeBaseURL resolves the agent runtime over the portless plane;
// FISSION_AGENTRUNTIME_URL overrides for hand-managed forwards and tests,
// exactly like workflowBaseURL (cmd/workflow/history.go) does for the
// workflow head.
func agentRuntimeBaseURL(input cli.Input, opts *cmd.CommandActioner) (string, error) {
	if u := os.Getenv("FISSION_AGENTRUNTIME_URL"); u != "" {
		return u, nil
	}
	localPort, err := util.SetupPortForwardToPort(input.Context(), opts.Client(),
		fissionNamespace(), "svc="+svcinfo.SvcAgentRuntime, svcinfo.PortAgentRuntime)
	if err != nil {
		return "", fmt.Errorf("port-forwarding to the agent runtime (is agentRuntime.enabled set?): %w", err)
	}
	return "http://127.0.0.1:" + localPort, nil
}

// fissionNamespace is where the Fission control plane lives; the env var
// wins, defaulting to "fission" (GetFissionNamespace returns "" when unset,
// which as a namespace argument silently queries nothing). Copied from
// cmd/workflow/history.go rather than imported — package-local by design, so
// cmd/agent does not reach across another command package.
func fissionNamespace() string {
	if ns := util.GetFissionNamespace(); ns != "" {
		return ns
	}
	return "fission"
}

func timeOrDash(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format(time.RFC3339)
}

func stringOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
