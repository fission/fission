// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"bytes"
	"encoding/json"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/flag"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

// RFC-0024 async dead-letter-queue admin CLI. It drives the router's DLQ admin API
// on the INTERNAL listener (HMAC-signed with FISSION_INTERNAL_AUTH_SECRET) rather
// than the Kubernetes clientset, because the dead-lettered state lives in the
// statestore behind the router, not in a CRD. These are the wire paths the router
// registers.
const (
	dlqAPIList    = "/v1/async/dlq/list"
	dlqAPIShow    = "/v1/async/dlq/show"
	dlqAPIRedrive = "/v1/async/dlq/redrive"
	dlqAPIPurge   = "/v1/async/dlq/purge"

	// dlqPageSize is the per-request page the CLI fetches while paging the list API
	// (matches the router's max limit), so list/redrive --all see the whole DLQ,
	// not just the first page.
	dlqPageSize = 1000
)

type dlqMessage struct {
	ID         string    `json:"id"`
	Namespace  string    `json:"namespace"`
	Function   string    `json:"function"`
	Reason     string    `json:"reason"`
	Attempts   int       `json:"attempts"`
	EnqueuedAt time.Time `json:"enqueuedAt"`
	DiedAt     time.Time `json:"diedAt"`
}

type dlqListResp struct {
	Messages  []dlqMessage `json:"messages"`
	NextToken string       `json:"nextToken"`
}

type dlqShowResp struct {
	dlqMessage
	Envelope json.RawMessage `json:"envelope"`
}

type dlqRedriveReq struct {
	IDs []string `json:"ids"`
}

type dlqMutateResp struct {
	Count int64 `json:"count"`
}

// DLQCommands builds the `fission function dlq` sub-group.
func DLQCommands() *cobra.Command {
	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "list",
		Short: "List dead-lettered async invocations",
	}, DLQList, flag.FlagSet{
		Optional: []flag.Flag{flag.Namespace, flag.DlqLimit, flag.Output},
	})
	showCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "show",
		Short: "Show the full envelope of one dead-lettered async invocation",
	}, DLQShow, flag.FlagSet{
		Required: []flag.Flag{flag.DlqID},
	})
	redriveCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "redrive",
		Short: "Re-enqueue dead-lettered async invocations for another delivery",
	}, DLQRedrive, flag.FlagSet{
		Optional: []flag.Flag{flag.DlqID, flag.DlqAll},
	})
	purgeCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "purge",
		Short: "Permanently delete every dead-lettered async invocation",
	}, DLQPurge, flag.FlagSet{})

	command := &cobra.Command{
		Use:   "dlq",
		Short: "Inspect and manage the async invocation dead-letter queue",
	}
	command.AddCommand(listCmd, showCmd, redriveCmd, purgeCmd)
	return command
}

type dlqSubCommand struct {
	cmd.CommandActioner
}

func DLQList(input cli.Input) error    { return (&dlqSubCommand{}).list(input) }
func DLQShow(input cli.Input) error    { return (&dlqSubCommand{}).show(input) }
func DLQRedrive(input cli.Input) error { return (&dlqSubCommand{}).redrive(input) }
func DLQPurge(input cli.Input) error   { return (&dlqSubCommand{}).purge(input) }

func (opts *dlqSubCommand) list(input cli.Input) error {
	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}
	limit := input.Int(flagkey.DlqLimit)
	msgs, more, err := opts.fetchDeadLetters(input, input.String(flagkey.Namespace), limit)
	if err != nil {
		return err
	}
	headers := []string{"ID", "NAMESPACE", "FUNCTION", "REASON", "ATTEMPTS", "DIED"}
	row := func(m dlqMessage) []string {
		return []string{m.ID, m.Namespace, m.Function, m.Reason, strconv.Itoa(m.Attempts), util.AgeOf(metav1.NewTime(m.DiedAt))}
	}
	if err := util.PrintObjects(format, msgs, headers, row, nil, func(dlqMessage) []string { return nil }); err != nil {
		return err
	}
	if more {
		fmt.Printf("\nShowing the first %d; raise --limit to see more.\n", len(msgs))
	}
	return nil
}

// fetchDeadLetters pages the DLQ list API, accumulating up to max messages (max <= 0
// means every one), filtered to namespace when set. It follows the API's nextToken
// so a large DLQ is fully traversed, not just its first page. The bool reports
// whether more messages remain beyond what was returned (only when max capped it).
func (opts *dlqSubCommand) fetchDeadLetters(input cli.Input, namespace string, max int) ([]dlqMessage, bool, error) {
	pageSize := dlqPageSize
	if max > 0 && max < pageSize {
		pageSize = max
	}
	var all []dlqMessage
	token := ""
	for {
		q := url.Values{"limit": {strconv.Itoa(pageSize)}}
		if namespace != "" {
			q.Set("namespace", namespace)
		}
		if token != "" {
			q.Set("token", token)
		}
		var resp dlqListResp
		if err := opts.call(input, http.MethodGet, dlqAPIList, q, nil, &resp); err != nil {
			return nil, false, err
		}
		all = append(all, resp.Messages...)
		if max > 0 && len(all) >= max {
			return all[:max], resp.NextToken != "" || len(all) > max, nil
		}
		if resp.NextToken == "" {
			return all, false, nil
		}
		token = resp.NextToken
	}
}

func (opts *dlqSubCommand) show(input cli.Input) error {
	q := url.Values{}
	q.Set("id", input.String(flagkey.DlqID))
	var resp dlqShowResp
	if err := opts.call(input, http.MethodGet, dlqAPIShow, q, nil, &resp); err != nil {
		return err
	}
	out, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

func (opts *dlqSubCommand) redrive(input cli.Input) error {
	ids, err := opts.redriveIDs(input)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return errors.New("nothing to redrive")
	}
	var resp dlqMutateResp
	if err := opts.call(input, http.MethodPost, dlqAPIRedrive, nil, dlqRedriveReq{IDs: ids}, &resp); err != nil {
		return err
	}
	fmt.Printf("redrove %d dead-lettered invocation(s)\n", resp.Count)
	return nil
}

// redriveIDs resolves the ids to redrive: a single --id, or every dead-lettered id
// under --all (paged from the list API). The two are mutually exclusive.
func (opts *dlqSubCommand) redriveIDs(input cli.Input) ([]string, error) {
	id := input.String(flagkey.DlqID)
	all := input.Bool(flagkey.DlqAll)
	switch {
	case id != "" && all:
		return nil, errors.New("--id and --all are mutually exclusive")
	case id != "":
		return []string{id}, nil
	case all:
		msgs, _, err := opts.fetchDeadLetters(input, "", 0) // 0 = every dead letter, all pages
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(msgs))
		for _, m := range msgs {
			ids = append(ids, m.ID)
		}
		return ids, nil
	default:
		return nil, errors.New("one of --id or --all is required")
	}
}

func (opts *dlqSubCommand) purge(input cli.Input) error {
	var resp dlqMutateResp
	if err := opts.call(input, http.MethodPost, dlqAPIPurge, nil, nil, &resp); err != nil {
		return err
	}
	fmt.Printf("purged %d dead-lettered invocation(s)\n", resp.Count)
	return nil
}

// call performs one DLQ API request against the router INTERNAL listener,
// HMAC-signing it with the ServiceRouterInternal key (from FISSION_INTERNAL_AUTH_SECRET,
// empty → pass-through) the same way `test --async` does, and decoding a JSON
// response into out (nil to ignore the body). The endpoints are on the internal
// listener precisely so they are never an unauthenticated public surface.
func (opts *dlqSubCommand) call(input cli.Input, method, path string, query url.Values, reqBody, out any) error {
	internalURL, err := util.GetRouterInternalURL(input.Context(), opts.Client())
	if err != nil {
		return fmt.Errorf("connecting to the Fission router internal listener: %w", err)
	}
	u := *internalURL
	u.Path = path
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	var body io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(input.Context(), method, u.String(), body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	transport := http.DefaultTransport
	if secret := os.Getenv("FISSION_INTERNAL_AUTH_SECRET"); secret != "" {
		transport = hmacauth.NewServiceSigningTransport([]byte(secret), hmacauth.ServiceRouterInternal, transport, "/v1/async/dlq/")
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return fmt.Errorf("calling the router DLQ API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotImplemented:
		return errors.New("async invocation is not enabled on this cluster")
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("router DLQ API rejected the request (%s); set FISSION_INTERNAL_AUTH_SECRET when authentication is enabled", resp.Status)
	case resp.StatusCode != http.StatusOK:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("router DLQ API returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding router DLQ response: %w", err)
		}
	}
	return nil
}
