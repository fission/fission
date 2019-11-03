/*
Copyright 2019 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package function

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/controller/client"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/httptrigger"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type TestSubCommand struct {
	client *client.Client
}

func Test(flags cli.Input) error {
	opts := TestSubCommand{
		client: cmd.GetServer(flags),
	}
	return opts.do(flags)
}

func (opts *TestSubCommand) do(flags cli.Input) error {
	m, err := cmd.GetMetadata("name", "fnNamespace", flags)
	if err != nil {
		return err
	}

	routerURL := os.Getenv("FISSION_ROUTER")
	if len(routerURL) == 0 {
		// Portforward to the fission router
		localRouterPort := util.SetupPortForward(util.GetFissionNamespace(),
			"application=fission-router")
		routerURL = "127.0.0.1:" + localRouterPort
	} else {
		routerURL = strings.TrimPrefix(routerURL, "http://")
	}

	fnUri := m.Name
	if m.Namespace != metav1.NamespaceDefault {
		fnUri = fmt.Sprintf("%v/%v", m.Namespace, m.Name)
	}

	functionUrl, err := url.Parse(fmt.Sprintf("http://%s/fission-function/%s", routerURL, fnUri))
	if err != nil {
		return err
	}
	queryParams := flags.StringSlice("query")
	if len(queryParams) > 0 {
		query := url.Values{}
		for _, q := range queryParams {
			queryParts := strings.SplitN(q, "=", 2)
			var key, value string
			if len(queryParts) == 0 {
				continue
			}
			if len(queryParts) > 0 {
				key = queryParts[0]
			}
			if len(queryParts) > 1 {
				value = queryParts[1]
			}
			query.Set(key, value)
		}
		functionUrl.RawQuery = query.Encode()
	}

	ctx := context.Background()
	if deadline := flags.Duration("timeout"); deadline > 0 {
		var closeCtx func()
		ctx, closeCtx = context.WithTimeout(ctx, deadline)
		defer closeCtx()
	}

	headers := flags.StringSlice("header")

	resp, err := doHTTPRequest(ctx, flags.String("method"), functionUrl.String(), flags.String("body"), headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return errors.Wrap(err, "error reading response from function")
	}

	if resp.StatusCode < 400 {
		fmt.Print(string(body))

		return nil
	}

	fmt.Printf("Error calling function %s: %d; Please try again or fix the error: %s", m.Name, resp.StatusCode, string(body))
	err = printPodLogs(flags)
	if err != nil {
		return Log(flags)
	}

	return nil
}

func doHTTPRequest(ctx context.Context, method, url, body string, headers []string) (*http.Response, error) {
	method, err := httptrigger.GetMethod(method)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		return nil, errors.Wrap(err, "error creating HTTP request")
	}

	for _, header := range headers {
		headerKeyValue := strings.SplitN(header, ":", 2)
		if len(headerKeyValue) != 2 {
			return nil, errors.New("failed to create request without appropriate headers")
		}
		req.Header.Set(headerKeyValue[0], headerKeyValue[1])
	}
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		return nil, errors.Wrap(err, "error executing HTTP request")
	}

	return resp, nil
}

func printPodLogs(flags cli.Input) error {
	fnName := flags.String("name")
	if len(fnName) == 0 {
		return errors.New("need --name argument.")
	}

	queryURL, err := url.Parse(util.GetServerUrl())
	if err != nil {
		return errors.Wrap(err, "error parsing the base URL")
	}
	queryURL.Path = fmt.Sprintf("/proxy/logs/%s", fnName)

	req, err := http.NewRequest("POST", queryURL.String(), nil)
	if err != nil {
		return errors.Wrap(err, "error creating logs request")
	}

	httpClient := http.Client{}
	resp, err := httpClient.Do(req)
	if err != nil {
		return errors.Wrap(err, "execute get logs request")
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("get logs from pod directly")
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return errors.Wrap(err, "read the response body")
	}

	fmt.Println(string(body))
	return nil
}
