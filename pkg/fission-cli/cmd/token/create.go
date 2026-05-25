// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package token

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type CreateSubCommand struct {
	cmd.CommandActioner
}

func Create(input cli.Input) error {
	return (&CreateSubCommand{}).do(input)
}

func (opts *CreateSubCommand) do(input cli.Input) error {
	return opts.run(input)
}

func (opts *CreateSubCommand) run(input cli.Input) error {

	lb := &fv1.AuthLogin{}

	username := input.String(flagkey.TokUsername)
	if len(username) != 0 {
		lb.Username = username
	}

	password := input.String(flagkey.TokPassword)
	if len(password) != 0 {
		lb.Password = password
	}

	values := map[string]string{"username": username, "password": password}

	jsonValue, _ := json.Marshal(values)

	authURI, _ := os.LookupEnv("FISSION_AUTH_URI")
	if input.IsSet(flagkey.TokAuthURI) {
		authURI = input.String(flagkey.TokAuthURI)
	}
	if len(authURI) == 0 {
		authURI = util.FISSION_AUTH_URI
	}
	routerURL, err := util.GetRouterURL(input.Context(), opts.Client())
	if err != nil {
		return fmt.Errorf("error getting router URL: %w", err)
	}
	authAuthenticatorUrl := routerURL.JoinPath(authURI)
	console.Verbose(2, "Auth URI: %s", authAuthenticatorUrl.String())
	resp, err := http.Post(authAuthenticatorUrl.String(), "application/json", bytes.NewBuffer(jsonValue))
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error creating token: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusCreated:
		var rat fv1.RouterAuthToken
		err = json.Unmarshal(body, &rat)
		if err != nil {
			return err
		}
		fmt.Println(rat.AccessToken)
	case http.StatusNotFound:
		fmt.Printf("%s. Please check if authentication is enabled and correct auth URI is mentioned via --authuri or FISSION_AUTH_URI.\n", resp.Status)
	default:
		fmt.Println(resp.Status)
	}

	return nil
}
