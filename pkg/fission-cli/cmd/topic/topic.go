// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package topic implements `fission topic publish|peek` — thin dev
// conveniences over the router's RFC-0027 topic admin API (the INTERNAL
// listener, HMAC-signed like `function dlq`), for exercising the zero-broker
// eventing loop without writing a publisher function.
package topic

import (
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
	"unicode/utf8"

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

// Wire paths the router registers (pkg/router/async_topics.go).
const (
	topicAPIPublish = "/v1/eventing/topic/publish"
	topicAPIPeek    = "/v1/eventing/topic/peek"
)

type topicEvent struct {
	Seq     int64     `json:"seq"`
	Type    string    `json:"type"`
	Payload []byte    `json:"payload"`
	At      time.Time `json:"at"`
}

type topicPeekResp struct {
	Head   int64        `json:"head"`
	Events []topicEvent `json:"events"`
}

// Commands builds the `fission topic` group.
func Commands() *cobra.Command {
	publishCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "publish",
		Short: "Publish an event to a topic (statestore direct, or a broker via egress)",
	}, Publish, flag.FlagSet{
		Required: []flag.Flag{flag.TopicName, flag.TopicData},
		Optional: []flag.Flag{flag.Namespace, flag.TopicContentType, flag.TopicMQType},
	})
	peekCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "peek",
		Short: "Show the most recent events on a statestore topic",
	}, Peek, flag.FlagSet{
		Required: []flag.Flag{flag.TopicName},
		Optional: []flag.Flag{flag.Namespace, flag.TopicLimit},
	})

	command := &cobra.Command{
		Use:     "topic",
		Aliases: []string{"topics"},
		Short:   "Publish to and inspect RFC-0027 eventing topics",
	}
	command.AddCommand(publishCmd, peekCmd)
	return command
}

type topicSubCommand struct {
	cmd.CommandActioner
}

func Publish(input cli.Input) error { return (&topicSubCommand{}).publish(input) }
func Peek(input cli.Input) error    { return (&topicSubCommand{}).peek(input) }

func (opts *topicSubCommand) publish(input cli.Input) error {
	namespace, err := opts.namespace(input)
	if err != nil {
		return err
	}
	q := url.Values{
		"namespace": {namespace},
		"topic":     {input.String(flagkey.TopicName)},
		"mqtype":    {input.String(flagkey.TopicMQType)},
	}
	resp, err := opts.call(input, http.MethodPost, topicAPIPublish, q,
		strings.NewReader(input.String(flagkey.TopicData)), input.String(flagkey.TopicContentType))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := checkStatus(resp); err != nil {
		return err
	}
	fmt.Printf("published to topic %q in namespace %q (%s)\n",
		input.String(flagkey.TopicName), namespace, input.String(flagkey.TopicMQType))
	return nil
}

func (opts *topicSubCommand) peek(input cli.Input) error {
	namespace, err := opts.namespace(input)
	if err != nil {
		return err
	}
	q := url.Values{
		"namespace": {namespace},
		"topic":     {input.String(flagkey.TopicName)},
		"limit":     {strconv.Itoa(input.Int(flagkey.TopicLimit))},
	}
	resp, err := opts.call(input, http.MethodGet, topicAPIPeek, q, nil, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := checkStatus(resp); err != nil {
		return err
	}
	var peek topicPeekResp
	if err := json.NewDecoder(resp.Body).Decode(&peek); err != nil {
		return fmt.Errorf("decoding router topic response: %w", err)
	}
	fmt.Printf("head: %d\n", peek.Head)
	headers := []string{"SEQ", "TYPE", "AGE", "PAYLOAD"}
	row := func(e topicEvent) []string {
		return []string{strconv.FormatInt(e.Seq, 10), e.Type, util.AgeOf(metav1.NewTime(e.At)), renderPayload(e.Payload)}
	}
	return util.PrintObjects(util.OutputTable, peek.Events, headers, row, nil, func(topicEvent) []string { return nil })
}

// renderPayload shows a printable payload verbatim (truncated) and sizes-only
// for binary ones.
func renderPayload(p []byte) string {
	const maxShow = 120
	if !utf8.Valid(p) {
		return fmt.Sprintf("<%d bytes binary>", len(p))
	}
	s := string(p)
	if len(s) > maxShow {
		return s[:maxShow] + "..."
	}
	return s
}

func (opts *topicSubCommand) namespace(input cli.Input) (string, error) {
	_, namespace, err := opts.GetResourceNamespace(input)
	return namespace, err
}

// call performs one topic API request against the router INTERNAL listener,
// HMAC-signed with the ServiceRouterInternal key (FISSION_INTERNAL_AUTH_SECRET,
// empty → pass-through) — the `function dlq` pattern.
func (opts *topicSubCommand) call(input cli.Input, method, path string, query url.Values, body io.Reader, contentType string) (*http.Response, error) {
	internalURL, err := util.GetRouterInternalURL(input.Context(), opts.Client())
	if err != nil {
		return nil, fmt.Errorf("connecting to the Fission router internal listener: %w", err)
	}
	u := *internalURL
	u.Path = path
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(input.Context(), method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	transport := http.DefaultTransport
	if secret := os.Getenv("FISSION_INTERNAL_AUTH_SECRET"); secret != "" {
		transport = hmacauth.NewServiceSigningTransport([]byte(secret), hmacauth.ServiceRouterInternal, transport, "/v1/eventing/")
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling the router topic API: %w", err)
	}
	return resp, nil
}

func checkStatus(resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusNotImplemented:
		return errors.New("eventing is not enabled on this cluster (requires the statestore)")
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("router topic API rejected the request (%s); set FISSION_INTERNAL_AUTH_SECRET when authentication is enabled", resp.Status)
	case resp.StatusCode != http.StatusOK:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("router topic API returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}
