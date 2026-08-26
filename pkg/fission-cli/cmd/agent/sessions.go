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
	"sort"
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
		Optional: []flag.Flag{flag.Namespace, flag.Output, flag.AgentToken, flag.AgentSessionsTree},
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

// parentRef mirrors agentruntime.ParentRef's wire shape (pkg/agentruntime/
// session.go) — the qualified <namespace>/<agent>/<id> reference to a
// spawning session. Kept as its own small mirror rather than importing
// ParentRef, for the same no-import reason sessionRecord below documents.
type parentRef struct {
	Namespace string `json:"namespace"`
	Agent     string `json:"agent"`
	ID        string `json:"id"`
}

// key returns p's canonical "<namespace>/<agent>/<id>" form — the same
// string agentruntime.ParentRef.String() produces, and the form --tree
// groups children by (a bare id is ambiguous across agents).
func (p parentRef) key() string {
	return p.Namespace + "/" + p.Agent + "/" + p.ID
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
	// Parent and Depth mirror SessionRecord's spawn-parentage fields
	// (pkg/agentruntime/session.go). Depth is tagged omitzero, matching the
	// server: json/v2's omitempty only drops null/""/{}/[], not a zero int,
	// so a root session's Depth (0) would otherwise appear on the wire (and
	// re-marshal, under --tree -o json) as an explicit "depth":0.
	Parent *parentRef `json:"parent,omitempty"`
	Depth  int        `json:"depth,omitzero"`
}

// key returns s's canonical "<namespace>/<agent>/<id>" form, the same shape
// parentRef.key() produces, so a child's Parent.key() looks it up directly.
func (s sessionRecord) key() string {
	return s.Namespace + "/" + s.Agent + "/" + s.ID
}

// sessionsResponse mirrors ListSessions's response envelope
// (pkg/agentruntime/registry_api.go sessionsResponse). Next (a pagination
// cursor) is not surfaced directly to the caller: the plain (non-`--tree`)
// `sessions list` fetches a single page, at maxSessionListLimit (below), and
// `--tree` instead consumes Next itself, via fetchAllSessionPages, to walk
// every page to exhaustion before rendering the family tree.
type sessionsResponse struct {
	Sessions []sessionRecord `json:"sessions"`
	Next     string          `json:"next,omitempty"`
}

// maxSessionListLimit mirrors agentruntime's own maxSessionListLimit
// (pkg/agentruntime/registry_api.go) — the server clamps any ?limit= above
// this back down to it, so asking for it directly is the largest single
// page this CLI can ever get.
const maxSessionListLimit = 500

// maxSessionPages bounds fetchAllSessionPages's page-cursor loop (500
// sessions per page => 50,000 sessions for one agent). Without a ceiling, a
// buggy server -- or a registry bug that returns a repeating cursor -- hangs
// `sessions list --tree` forever, looping on `next` with no forward
// progress.
const maxSessionPages = 100

type SessionsListSubCommand struct {
	cmd.CommandActioner
}

type SessionsGetSubCommand struct {
	cmd.CommandActioner
}

func SessionsList(input cli.Input) error { return (&SessionsListSubCommand{}).do(input) }
func SessionsGet(input cli.Input) error  { return (&SessionsGetSubCommand{}).do(input) }

func (opts *SessionsListSubCommand) do(input cli.Input) error {
	// Resolved before any request or stdout write: a structured format
	// (json/yaml) must never have anything else land on stdout ahead of it,
	// or `-o json | jq` breaks on the polluting line.
	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	agentName := input.String(flagkey.FnName)
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error resolving namespace: %w", err)
	}

	base, err := agentRuntimeBaseURL(input, &opts.CommandActioner)
	if err != nil {
		return err
	}

	if input.Bool(flagkey.AgentSessionsTree) {
		return listSessionsTree(input, format, base, namespace, agentName)
	}

	// limit=maxSessionListLimit (the server's cap, pkg/agentruntime/registry_api.go):
	// this CLI fetches a single page and does not follow Next, so asking for
	// the server's cap rather than its lower default raises the truncation
	// threshold within that single-GET scope.
	reqURL := fmt.Sprintf("%s/registry/agents/%s/%s/sessions?limit=%d", base,
		url.PathEscape(namespace), url.PathEscape(agentName), maxSessionListLimit)

	body, err := agentRuntimeGet(input, reqURL)
	if err != nil {
		return sessionsFriendlyError(err, namespace, agentName)
	}

	var resp sessionsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("decoding sessions response: %w", err)
	}
	if resp.Next != "" && (format == util.OutputTable || format == util.OutputWide) {
		// The truncation notice is only a human-readable-table courtesy: a
		// structured format must decode as exactly the server's payload, so
		// json/yaml stay silent even when more pages exist.
		console.Warn("more sessions exist beyond this page; sessions list does not yet page through them")
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

// listSessionsTree implements `sessions list --tree`: unlike the plain list
// above, it loops the server's page cursor to exhaustion first — a
// truncated page would show orphaned children (a child whose parent landed
// on an earlier, un-fetched page looks parentless) and hide parents (a
// parent past the fetched page never appears at all) — then renders the
// spawn family as an indented tree.
func listSessionsTree(input cli.Input, format util.OutputFormat, base, namespace, agentName string) error {
	sessions, err := fetchAllSessionPages(input, base, namespace, agentName)
	if err != nil {
		return sessionsFriendlyError(err, namespace, agentName)
	}

	// --tree's only effect on a structured format is the exhaustive fetch
	// above: json/yaml already render the full flat array PrintStructured
	// produces for the plain list, just complete instead of one page, and
	// carry no console.Warn (there is nothing left un-fetched to warn about).
	if handled, err := util.PrintStructured(format, sessions); err != nil || handled {
		return err
	}

	// OutputWide has no extra tree-specific column; it renders identically
	// to the default table.
	printSessionTree(os.Stdout, sessions)
	return nil
}

// fetchAllSessionPages GETs every page of an agent's sessions, following the
// server's "next" cursor (the "page" query param, mirroring registry_api.go)
// until it comes back empty, or until maxSessionPages is reached — whichever
// comes first. Hitting the ceiling returns what was fetched so far (never an
// error: a partial family tree is more useful than none) with a warning on
// stderr; the caller gets a possibly-incomplete set, exactly the same shape
// a page-fetch error would have left it in.
func fetchAllSessionPages(input cli.Input, base, namespace, agentName string) ([]sessionRecord, error) {
	var all []sessionRecord
	page := ""
	for i := 0; i < maxSessionPages; i++ {
		reqURL := fmt.Sprintf("%s/registry/agents/%s/%s/sessions?limit=%d", base,
			url.PathEscape(namespace), url.PathEscape(agentName), maxSessionListLimit)
		if page != "" {
			reqURL += "&page=" + url.QueryEscape(page)
		}

		body, err := agentRuntimeGet(input, reqURL)
		if err != nil {
			return nil, err
		}
		var resp sessionsResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("decoding sessions response: %w", err)
		}
		all = append(all, resp.Sessions...)
		if resp.Next == "" {
			return all, nil
		}
		page = resp.Next
	}
	console.Warn(fmt.Sprintf("sessions list --tree: stopped after %d pages with more sessions remaining (server keeps returning a cursor); showing a partial, possibly-incomplete family tree", maxSessionPages))
	return all, nil
}

// printSessionTree renders sessions as an indented parent/child tree to w.
// Children are keyed by their parent's full <namespace>/<agent>/<id> triple
// (parentRef.key(); a bare id is ambiguous across agents) rather than by
// object identity, so the walk below is a pure lookup over that map.
//
// Three groups, all guaranteed visible (never silently dropped):
//  1. roots (Parent == nil), printed with their reachable descendants;
//  2. a child whose parent triple was not among the fetched records renders
//     under an explicit "(unknown: <ref>)" node instead of vanishing;
//  3. anything still unprinted after (1) and (2) has a parent that IS among
//     the fetched records but is unreachable from any root — a self-parent
//     or a cycle in a buggy/hostile server response — and is rendered as its
//     own top-level entry rather than dropped or looped over forever.
//
// Every row's indent is the record's own Depth field (already the absolute
// spawn-chain depth the server computed at creation time), not a depth
// recomputed from how much of the tree this walk could reconstruct — so an
// orphan under an "(unknown: ...)" node still indents at its true depth.
func printSessionTree(w io.Writer, sessions []sessionRecord) {
	tw := util.NewTabWriter(w)
	fmt.Fprintln(tw, strings.Join([]string{"ID", "STATUS", "DEPTH", "TURNS", "CONTINUATIONS", "LAST-ACTIVE", "POD"}, "\t"))

	byKey := make(map[string]sessionRecord, len(sessions))
	childrenOf := make(map[string][]sessionRecord)
	var roots []sessionRecord
	for _, s := range sessions {
		byKey[s.key()] = s
		if s.Parent == nil {
			roots = append(roots, s)
		} else {
			pk := s.Parent.key()
			childrenOf[pk] = append(childrenOf[pk], s)
		}
	}

	byID := func(recs []sessionRecord) {
		sort.Slice(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
	}
	byID(roots)
	for k := range childrenOf {
		byID(childrenOf[k])
	}

	printed := make(map[string]bool, len(sessions))
	var printSubtree func(s sessionRecord)
	printSubtree = func(s sessionRecord) {
		key := s.key()
		if printed[key] {
			return // already printed via another path (e.g. a cycle) — never twice.
		}
		printed[key] = true
		writeSessionTreeRow(tw, s)
		for _, child := range childrenOf[key] {
			printSubtree(child)
		}
	}

	for _, root := range roots {
		printSubtree(root)
	}

	var unknownParents []string
	for pk := range childrenOf {
		if _, ok := byKey[pk]; !ok {
			unknownParents = append(unknownParents, pk)
		}
	}
	sort.Strings(unknownParents)
	for _, pk := range unknownParents {
		fmt.Fprintf(tw, "(unknown: %s)\t-\t-\t-\t-\t-\t-\n", pk)
		for _, child := range childrenOf[pk] {
			printSubtree(child)
		}
	}

	var leftover []sessionRecord
	for _, s := range sessions {
		if !printed[s.key()] {
			leftover = append(leftover, s)
		}
	}
	byID(leftover)
	for _, s := range leftover {
		printSubtree(s)
	}

	tw.Flush()
}

// writeSessionTreeRow writes one session's row, indenting its ID cell two
// spaces per level of s.Depth.
func writeSessionTreeRow(w io.Writer, s sessionRecord) {
	fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%s\t%s\n",
		strings.Repeat("  ", s.Depth)+s.ID,
		s.Status,
		s.Depth,
		s.Stats.Turns,
		s.Stats.Continuations,
		timeOrDash(s.LastActiveAt),
		stringOrDash(s.CurrentPod),
	)
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
