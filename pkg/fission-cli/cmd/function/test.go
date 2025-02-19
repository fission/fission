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
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"errors"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/httptrigger"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

type TestSubCommand struct {
	cmd.CommandActioner
}

func Test(input cli.Input) error {
	return (&TestSubCommand{}).do(input)
}

func (opts *TestSubCommand) do(input cli.Input) error {
	fnName := input.String(flagkey.FnName)
	_, namespace, err := opts.GetResourceNamespace(input, flagkey.NamespaceFunction)
	if err != nil {
		return fmt.Errorf("error in testing function : %w", err)
	}

	function, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).Get(input.Context(), fnName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read function '%s': %w", fnName, err)
	}

	m := &metav1.ObjectMeta{
		Name:      fnName,
		Namespace: namespace,
	}

	routerURL, err := util.GetRouterURL(input.Context(), opts.Client())
	if err != nil {
		return fmt.Errorf("error getting router URL: %w", err)
	}
	fnURI := util.UrlForFunction(m.Name, m.Namespace)
	if input.IsSet(flagkey.FnSubPath) {
		subPath := input.String(flagkey.FnSubPath)
		if !strings.HasPrefix(subPath, "/") {
			fnURI = fnURI + "/" + subPath
		} else {
			fnURI = fnURI + subPath
		}
	}
	fnURL := routerURL.JoinPath(fnURI)
	console.Verbose(2, "Function test url: %v", fnURL.String())

	queryParams := input.StringSlice(flagkey.FnTestQuery)
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
		fnURL.RawQuery = query.Encode()
	}

	var (
		ctx        context.Context
		reqTimeout time.Duration
	)

	fnTestTimeout := input.Duration(flagkey.FnTestTimeout)
	fnSpecTimeout := time.Duration(function.Spec.FunctionTimeout) * time.Second

	if input.IsSet(flagkey.FnTestTimeout) && (fnTestTimeout < fnSpecTimeout) {
		reqTimeout = fnTestTimeout
		console.Warn(fmt.Sprintf("timeout specified is less than functionTimeout %d Overriding value to %d", fnTestTimeout, fnSpecTimeout))
	} else {
		reqTimeout = fnSpecTimeout
	}

	if reqTimeout <= 0*time.Second {
		ctx = input.Context()
	} else {
		var closeCtx context.CancelFunc
		ctx, closeCtx = context.WithTimeoutCause(input.Context(), reqTimeout, fmt.Errorf("function request timeout (%d)s exceeded", reqTimeout))
		defer closeCtx()
	}

	methods := input.StringSlice(flagkey.HtMethod)
	if len(methods) == 0 {
		return errors.New("HTTP method not mentioned")
	} else if len(methods) > 1 {
		return errors.New("More than one HTTP method not supported")
	}
	method, err := httptrigger.GetMethod(methods[0])
	if err != nil {
		return err
	}
	resp, err := doHTTPRequest(ctx, fnURL.String(),
		input.StringSlice(flagkey.FnTestHeader),
		method,
		input.String(flagkey.FnTestBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading response from function: %w", err)
	}

	if resp.StatusCode < 400 {
		os.Stdout.Write(body)
		return nil
	}

	console.Errorf("calling function %s: %d; Please try again or fix the error: %s\n", m.Name, resp.StatusCode, string(body))
	err = util.FunctionPodLogs(input.Context(), m.Name, m.Namespace, opts.Client())
	if err != nil {
		console.Errorf("getting function logs: %v. Try to get logs from log database.", err)
		err = Log(input)
		if err != nil {
			console.Errorf("getting function logs from log database: %v", err)
		}
	}
	return errors.New("error getting function response")
}

func doHTTPRequest(ctx context.Context, url string, headers []string, method, body string) (*http.Response, error) {
	shutdown, err := otelUtils.InitProvider(ctx, nil, "fission-cli")
	if err != nil {
		return nil, err
	}
	if shutdown != nil {
		defer shutdown(ctx)
	}

	tracer := otel.Tracer("fission-cli")
	ctx, span := tracer.Start(ctx, "httpRequest")
	defer span.End()

	method, err = httptrigger.GetMethod(method)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("error creating HTTP request: %w", err)
	}

	accesstoken, ok := os.LookupEnv(util.FISSION_AUTH_TOKEN)
	if ok && len(accesstoken) != 0 {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %v", accesstoken))
	}

	for _, header := range headers {
		headerKeyValue := strings.SplitN(header, ":", 2)
		if len(headerKeyValue) != 2 {
			return nil, errors.New("failed to create request without appropriate headers")
		}
		req.Header.Set(headerKeyValue[0], headerKeyValue[1])
	}

	if console.Verbosity >= 2 {
		dumpReq, err := httputil.DumpRequestOut(req, false)
		if err != nil {
			return nil, err
		}
		console.Verbose(2, string(dumpReq))
	}

	hc := &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}
	resp, err := hc.Do(req.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("error executing HTTP request: %w", err)
	}

	if console.Verbosity >= 2 {
		dumpRes, err := httputil.DumpResponse(resp, false)
		if err != nil {
			return nil, err
		}
		console.Verbose(2, string(dumpRes))
	}

	return resp, nil
}
