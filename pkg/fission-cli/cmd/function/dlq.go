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

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/flag"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

// RFC-0024 async dead-letter-queue admin CLI. It drives the router's DLQ admin API
// over HTTP (util.GetRouterURL + a Bearer FISSION_AUTH_TOKEN) rather than the
// Kubernetes clientset, because the dead-lettered state lives in the statestore
// behind the router, not in a CRD. These are the wire paths the router registers.
const (
	dlqAPIList    = "/v1/async/dlq/list"
	dlqAPIShow    = "/v1/async/dlq/show"
	dlqAPIRedrive = "/v1/async/dlq/redrive"
	dlqAPIPurge   = "/v1/async/dlq/purge"
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
	q := url.Values{}
	if ns := input.String(flagkey.Namespace); ns != "" {
		q.Set("namespace", ns)
	}
	if input.IsSet(flagkey.DlqLimit) {
		q.Set("limit", strconv.Itoa(input.Int(flagkey.DlqLimit)))
	}
	var resp dlqListResp
	if err := opts.call(input, http.MethodGet, dlqAPIList, q, nil, &resp); err != nil {
		return err
	}
	headers := []string{"ID", "NAMESPACE", "FUNCTION", "REASON", "ATTEMPTS", "DIED"}
	row := func(m dlqMessage) []string {
		return []string{m.ID, m.Namespace, m.Function, m.Reason, strconv.Itoa(m.Attempts), util.AgeOf(metav1.NewTime(m.DiedAt))}
	}
	if err := util.PrintObjects(format, resp.Messages, headers, row, nil, func(dlqMessage) []string { return nil }); err != nil {
		return err
	}
	if resp.NextToken != "" {
		fmt.Printf("\nMore results available; narrow with a smaller page or re-run to continue.\n")
	}
	return nil
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
		var ids []string
		var resp dlqListResp
		if err := opts.call(input, http.MethodGet, dlqAPIList, url.Values{"limit": {"1000"}}, nil, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Messages {
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

// call performs one router DLQ API request, signing it with the Bearer auth token
// (empty when auth is disabled) and decoding a JSON response into out (nil to
// ignore the body).
func (opts *dlqSubCommand) call(input cli.Input, method, path string, query url.Values, reqBody, out any) error {
	routerURL, err := util.GetRouterURL(input.Context(), opts.Client())
	if err != nil {
		return fmt.Errorf("connecting to the Fission router: %w", err)
	}
	u := *routerURL
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
	req.Header.Set("Authorization", "Bearer "+os.Getenv(util.FISSION_AUTH_TOKEN))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("calling the router DLQ API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotImplemented:
		return errors.New("async invocation is not enabled on this cluster")
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("router DLQ API rejected the request (%s); set %s if authentication is enabled", resp.Status, util.FISSION_AUTH_TOKEN)
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
