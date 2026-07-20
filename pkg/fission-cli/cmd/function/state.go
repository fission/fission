// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

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

// RFC-0023 keyed-state admin CLI. It drives statesvc's admin channel
// (ServiceStateAPI HMAC, from FISSION_INTERNAL_AUTH_SECRET) rather than the
// Kubernetes clientset, because state lives in the statestore behind
// statesvc, not in a CRD. The admin path FAILS CLOSED: without the secret,
// statesvc answers 401 and this CLI refuses up front with the same guidance.
const (
	stateHeaderNamespace = "X-Fission-State-Namespace"
	stateHeaderKeyspace  = "X-Fission-State-Keyspace"
	stateHeaderVersion   = "X-Fission-State-Version"
	stateHeaderTTL       = "X-Fission-State-TTL"
)

// StateCommands builds the `fission function state` sub-group.
func StateCommands() *cobra.Command {
	getCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "get",
		Short: "Get a key from a function's state keyspace",
	}, StateGet, flag.FlagSet{
		Required: []flag.Flag{flag.FnName, flag.StateKey},
		Optional: []flag.Flag{flag.Namespace},
	})
	setCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "set",
		Short: "Set a key in a function's state keyspace",
	}, StateSet, flag.FlagSet{
		Required: []flag.Flag{flag.FnName, flag.StateKey, flag.StateValue},
		Optional: []flag.Flag{flag.Namespace, flag.StateTTL, flag.StateIfVersion},
	})
	deleteCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "delete",
		Short: "Delete a key from a function's state keyspace",
	}, StateDelete, flag.FlagSet{
		Required: []flag.Flag{flag.FnName, flag.StateKey},
		Optional: []flag.Flag{flag.Namespace, flag.StateIfVersion},
	})
	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "list",
		Short: "List keys in a function's state keyspace",
	}, StateList, flag.FlagSet{
		Required: []flag.Flag{flag.FnName},
		Optional: []flag.Flag{flag.Namespace, flag.StatePrefix},
	})

	command := &cobra.Command{
		Use:   "state",
		Short: "Inspect and manage a function's keyed state (RFC-0023)",
	}
	command.AddCommand(getCmd, setCmd, deleteCmd, listCmd)
	return command
}

type stateSubCommand struct {
	cmd.CommandActioner
}

func StateGet(input cli.Input) error    { return (&stateSubCommand{}).get(input) }
func StateSet(input cli.Input) error    { return (&stateSubCommand{}).set(input) }
func StateDelete(input cli.Input) error { return (&stateSubCommand{}).del(input) }
func StateList(input cli.Input) error   { return (&stateSubCommand{}).list(input) }

func (opts *stateSubCommand) get(input cli.Input) error {
	resp, err := opts.call(input, http.MethodGet, input.String(flagkey.StateKey), "", nil, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := stateStatusErr(resp); err != nil {
		return err
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "version: %s\n", resp.Header.Get(stateHeaderVersion))
	fmt.Println(string(body))
	return nil
}

func (opts *stateSubCommand) set(input cli.Input) error {
	hdrs := map[string]string{}
	if ttl := input.Duration(flagkey.StateTTL); ttl > 0 {
		hdrs[stateHeaderTTL] = ttl.String()
	}
	if input.IsSet(flagkey.StateIfVersion) {
		hdrs["If-Match"] = strconv.Itoa(input.Int(flagkey.StateIfVersion))
	}
	resp, err := opts.call(input, http.MethodPut, input.String(flagkey.StateKey), "",
		strings.NewReader(input.String(flagkey.StateValue)), hdrs)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := stateStatusErr(resp); err != nil {
		return err
	}
	fmt.Println("key set")
	return nil
}

func (opts *stateSubCommand) del(input cli.Input) error {
	hdrs := map[string]string{}
	if input.IsSet(flagkey.StateIfVersion) {
		hdrs["If-Match"] = strconv.Itoa(input.Int(flagkey.StateIfVersion))
	}
	resp, err := opts.call(input, http.MethodDelete, input.String(flagkey.StateKey), "", nil, hdrs)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := stateStatusErr(resp); err != nil {
		return err
	}
	fmt.Println("key deleted")
	return nil
}

func (opts *stateSubCommand) list(input cli.Input) error {
	cursor := ""
	total := 0
	for {
		q := url.Values{}
		if p := input.String(flagkey.StatePrefix); p != "" {
			q.Set("prefix", p)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		resp, err := opts.call(input, http.MethodGet, "", q.Encode(), nil, nil)
		if err != nil {
			return err
		}
		if err := stateStatusErr(resp); err != nil {
			_ = resp.Body.Close()
			return err
		}
		var page struct {
			Keys   []string `json:"keys"`
			Cursor string   `json:"cursor"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("decoding statesvc response: %w", err)
		}
		for _, k := range page.Keys {
			fmt.Println(k)
		}
		total += len(page.Keys)
		if page.Cursor == "" {
			if total == 0 {
				fmt.Fprintln(os.Stderr, "no keys")
			}
			return nil
		}
		cursor = page.Cursor
	}
}

// call performs one state API request against statesvc's admin channel for
// the function's effective keyspace. key == "" targets the listing endpoint.
func (opts *stateSubCommand) call(input cli.Input, method, key, rawQuery string, body io.Reader, hdrs map[string]string) (*http.Response, error) {
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return nil, fmt.Errorf("error resolving namespace: %w", err)
	}
	fnName := input.String(flagkey.FnName)
	fn, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).Get(input.Context(), fnName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error getting function: %w", err)
	}
	if fn.Spec.State == nil {
		return nil, fmt.Errorf("function %s/%s has no state config (spec.state); nothing to inspect", namespace, fnName)
	}
	keyspace := fn.Spec.State.EffectiveKeyspace(fn.Name)

	secret := os.Getenv("FISSION_INTERNAL_AUTH_SECRET")
	if secret == "" {
		return nil, errors.New("fn state requires FISSION_INTERNAL_AUTH_SECRET (the statesvc admin channel fails closed); read it with: kubectl get secret fission-internal-auth -n fission -o jsonpath='{.data.secret}' | base64 -d")
	}

	baseURL, err := util.GetStateSvcURL(input.Context(), opts.Client())
	if err != nil {
		return nil, fmt.Errorf("connecting to statesvc: %w", err)
	}
	u := *baseURL
	u.Path = "/v1/state"
	if key != "" {
		u.Path += "/" + key
	}
	u.RawQuery = rawQuery

	req, err := http.NewRequestWithContext(input.Context(), method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set(stateHeaderNamespace, namespace)
	req.Header.Set(stateHeaderKeyspace, keyspace)
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}

	transport := hmacauth.NewServiceSigningTransport([]byte(secret), hmacauth.ServiceStateAPI, http.DefaultTransport, "/v1/state")
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling statesvc: %w", err)
	}
	return resp, nil
}

// stateStatusErr maps statesvc's machine-readable errors to CLI errors.
func stateStatusErr(resp *http.Response) error {
	if resp.StatusCode < 400 {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	var e struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	detail := strings.TrimSpace(string(msg))
	if json.Unmarshal(msg, &e) == nil && e.Error != "" {
		detail = e.Error
	}
	switch resp.StatusCode {
	case http.StatusNotFound:
		return errors.New("key not found")
	case http.StatusPreconditionFailed:
		return fmt.Errorf("version conflict: %s", detail)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("statesvc rejected the request (%s): %s", resp.Status, detail)
	default:
		return fmt.Errorf("statesvc returned %s: %s", resp.Status, detail)
	}
}
