// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"context"
	"encoding/json"
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

	"github.com/go-logr/logr"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/httptrigger"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/router/asyncinvoke"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/correlation"
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
	_, namespace, err := opts.GetResourceNamespace(input)
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
	fnURI := utils.UrlForFunction(m.Name, m.Namespace)
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
		return errors.New("more than one HTTP method not supported")
	}
	method, err := httptrigger.GetMethod(methods[0])
	if err != nil {
		return err
	}
	if input.Bool(flagkey.FnTestAsync) {
		return opts.invokeAsync(ctx, input, m, method)
	}
	resp, err := DoHTTPRequest(ctx, fnURL.String(),
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

	// Echo the per-invocation request id (RFC-0015) to stderr — keeping stdout
	// clean for the function body — so the developer can correlate this call.
	reqID := resp.Header.Get(correlation.HeaderRequestID)
	if reqID != "" {
		fmt.Fprintf(os.Stderr, "Request ID: %s\n", reqID)
	}

	if resp.StatusCode < 400 {
		os.Stdout.Write(body)
		return nil
	}

	// On failure, render the structured RFC-0015 attribution when the router
	// produced one (X-Fission-Component header), else the legacy raw body.
	renderInvocationFailure(os.Stderr, m.Name, resp.StatusCode,
		resp.Header.Get(correlation.HeaderComponent), body)

	err = util.FunctionPodLogs(input.Context(), m.Name, m.Namespace, opts.Client())
	if err != nil {
		console.Errorf("getting function logs: %v. Try to get logs from log database.", err)
		err = Log(input)
		if err != nil {
			console.Errorf("getting function logs from log database: %v", err)
		}
	}
	if reqID != "" {
		fmt.Fprintf(os.Stderr, "Correlated logs: fission function logs --name %s --request-id %s --dbtype loki\n", m.Name, reqID)
	}
	return errors.New("error getting function response")
}

// invokeAsync runs `fission function test --async` (RFC-0024): it POSTs to the
// function on the router INTERNAL listener with X-Fission-Invoke-Mode: async,
// HMAC-signing the request (ServiceRouterInternal) when FISSION_INTERNAL_AUTH_SECRET
// is set so the internal verifier accepts it. The router returns 202 with the
// durable invocation id, which is printed; the response body is not awaited.
func (opts *TestSubCommand) invokeAsync(ctx context.Context, input cli.Input, m *metav1.ObjectMeta, method string) error {
	internalURL, err := util.GetRouterInternalURL(input.Context(), opts.Client())
	if err != nil {
		return fmt.Errorf("resolving the router internal listener: %w", err)
	}
	fnURI := utils.UrlForFunction(m.Name, m.Namespace)
	if input.IsSet(flagkey.FnSubPath) {
		subPath := input.String(flagkey.FnSubPath)
		if !strings.HasPrefix(subPath, "/") {
			fnURI += "/"
		}
		fnURI += subPath
	}
	fnURL := internalURL.JoinPath(fnURI)
	if q := testQueryValues(input); len(q) > 0 {
		fnURL.RawQuery = q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, fnURL.String(), strings.NewReader(input.String(flagkey.FnTestBody)))
	if err != nil {
		return err
	}
	req.Header.Set(asyncinvoke.HeaderInvokeMode, asyncinvoke.InvokeModeAsync)
	for _, h := range input.StringSlice(flagkey.FnTestHeader) {
		if k, v, ok := strings.Cut(h, ":"); ok {
			req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}

	var transport http.RoundTripper = otelhttp.NewTransport(http.DefaultTransport)
	if secret := os.Getenv("FISSION_INTERNAL_AUTH_SECRET"); secret != "" {
		transport = hmacauth.NewServiceSigningTransport([]byte(secret), hmacauth.ServiceRouterInternal, transport, "/fission-function/")
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	switch resp.StatusCode {
	case http.StatusAccepted:
		id := resp.Header.Get(asyncinvoke.HeaderInvocationID)
		if id == "" {
			var decoded map[string]string
			if json.Unmarshal(body, &decoded) == nil {
				id = decoded["invocationId"]
			}
		}
		if id == "" {
			// The router always returns an id (invariant A1); an empty one means a
			// proxy stripped the header and mangled the body — accepted, but untrackable.
			fmt.Fprintln(os.Stderr, "warning: accepted (202) but no invocation id was returned")
			return nil
		}
		fmt.Printf("Accepted (202)\ninvocationId: %s\n", id)
		return nil
	case http.StatusNotImplemented:
		return errors.New("async invocation is not enabled on this cluster")
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("router rejected the async request (%s); set FISSION_INTERNAL_AUTH_SECRET when authentication is enabled", resp.Status)
	default:
		return fmt.Errorf("async invocation failed (%s): %s", resp.Status, strings.TrimSpace(string(body)))
	}
}

// testQueryValues builds the request query from repeated --query key=value flags.
func testQueryValues(input cli.Input) url.Values {
	query := url.Values{}
	for _, q := range input.StringSlice(flagkey.FnTestQuery) {
		key, value, _ := strings.Cut(q, "=")
		if key == "" {
			continue
		}
		query.Set(key, value)
	}
	return query
}

// renderInvocationFailure writes a one-line diagnosis for a failed `function
// test`. When the router attributed the failure (RFC-0015), signalled by the
// X-Fission-Component header, it names the responsible component, the stable
// reason, and the request id; otherwise it falls back to the raw response body,
// so it degrades cleanly against a server that predates RFC-0015.
func renderInvocationFailure(out io.Writer, fnName string, statusCode int, component string, body []byte) {
	if component == "" {
		fail(out, "✗ function %q returned %d: %s", fnName, statusCode, strings.TrimSpace(string(body)))
		return
	}
	var ie ferror.InvocationError
	_ = json.Unmarshal(body, &ie) // body may be empty/non-JSON; the header is authoritative for the component
	line := fmt.Sprintf("✗ function %q failed in %s", fnName, component)
	if ie.Reason != "" {
		line += fmt.Sprintf(" (%s)", ie.Reason)
	}
	line += fmt.Sprintf(" — status %d", statusCode)
	if ie.RequestID != "" {
		line += fmt.Sprintf(", request %s", ie.RequestID)
	}
	fail(out, "%s", line)
	if ie.Message != "" {
		note(out, "  detail: %s", ie.Message)
	}
}

// DoHTTPRequest performs the shared CLI function-invoke request: it sets up the
// OTel transport, injects the auth token and the caller's headers, optionally
// dumps the request/response in verbose mode, and returns the response. Shared
// by `function test` and `function run` (RFC-0018) so the local and in-cluster
// invoke paths are byte-identical.
func DoHTTPRequest(ctx context.Context, url string, headers []string, method, body string) (*http.Response, error) {
	shutdown, err := otelUtils.InitProvider(ctx, logr.Logger{}, "fission-cli")
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
		console.Verbose(2, "%s", string(dumpReq))
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
		console.Verbose(2, "%s", string(dumpRes))
	}

	return resp, nil
}
