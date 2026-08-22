// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

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

	// RFC-0025: --alias/--version smoke-test a specific FunctionAlias/pinned
	// FunctionVersion instead of the live function. Resolved to a router
	// route suffix ("<name>:<suffix>", see utils.UrlForFunctionRef) below;
	// the preflight Get here exists purely for CLI UX -- a typo'd
	// --alias/--version would otherwise surface only as an opaque router 404
	// once the request is actually sent (see the StatusNotFound handling
	// further down).
	suffix, err := opts.resolveTestRefSuffix(input, fnName, namespace)
	if err != nil {
		return err
	}

	m := &metav1.ObjectMeta{
		Name:      fnName,
		Namespace: namespace,
	}

	var (
		ctx        context.Context
		reqTimeout time.Duration
	)

	fnTestTimeout := input.Duration(flagkey.FnTestTimeout)
	fnSpecTimeout := time.Duration(function.Spec.FunctionTimeout) * time.Second

	if input.IsSet(flagkey.FnTestTimeout) && (fnTestTimeout < fnSpecTimeout) {
		reqTimeout = fnTestTimeout
		console.Warn(fmt.Sprintf("test timeout %s is less than functionTimeout %s; using the smaller test timeout", fnTestTimeout, fnSpecTimeout))
	} else {
		reqTimeout = fnSpecTimeout
	}

	if reqTimeout <= 0*time.Second {
		ctx = input.Context()
	} else {
		var closeCtx context.CancelFunc
		ctx, closeCtx = context.WithTimeoutCause(input.Context(), reqTimeout, fmt.Errorf("function request timeout (%s) exceeded", reqTimeout.String()))
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
	isAsync := input.Bool(flagkey.FnTestAsync)

	// Sync and async both hit the router INTERNAL listener (port 8889,
	// svc/router-internal) for /fission-function/<ns>/<name> — the public
	// listener (8888) no longer serves that path after GHSA-3g33-6vg6-27m8.
	// HMAC signing is applied when FISSION_INTERNAL_AUTH_SECRET is set; empty
	// secret = pass-through (matches the chart's internalAuth.enabled=false).
	//
	// --async note: fnURL (built below, suffix included) is the one URL both
	// the sync and async branches send to -- there is no separate async-only
	// URL construction. So when --alias/--version pins this call to a
	// suffixed route, the async enqueue itself happens off *that* resolved
	// route: the pin applies at enqueue time, not just to the initial
	// dispatch. A later alias move does not retarget an already-enqueued
	// invocation.
	internalURL, err := util.GetRouterInternalURL(input.Context(), opts.Client())
	if err != nil {
		return fmt.Errorf("resolving the router internal listener: %w", err)
	}

	fnURI := utils.UrlForFunctionRef(m.Name, m.Namespace, suffix)
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

	invokeMode := ""
	if isAsync {
		// Set the invoke-mode header LAST (inside combinedHTTPRequest) so
		// --async stays authoritative over a user -H "X-Fission-Invoke-Mode: sync".
		invokeMode = asyncinvoke.InvokeModeAsync
	}
	invokeOpts := invokeOptions{
		Method:            method,
		URL:               fnURL.String(),
		Headers:           input.StringSlice(flagkey.FnTestHeader),
		Body:              input.String(flagkey.FnTestBody),
		SignWithHMAC:      true,
		AttachBearerToken: false,
		InvokeModeHeader:  invokeMode,
	}
	resp, err := combinedHTTPRequest(ctx, invokeOpts)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if isAsync {
		return handleAsyncResponse(resp, fnName, suffix)
	}

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
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("router rejected the request (%s); set FISSION_INTERNAL_AUTH_SECRET when authentication is enabled", resp.Status)
	case http.StatusNotFound:
		if msg, ok := suffixedRouteNotFoundError(fnName, suffix, resp.Header.Get(utils.HeaderRouteMiss) != ""); ok {
			return errors.New(msg)
		}
		fallthrough
	default:
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
}

// resolveTestRefSuffix reads --alias/--version (mutually exclusive) and
// resolves the router route suffix (RFC-0025) `fission function test` should
// target: "" for the live function, else the alias or version name. Both
// flags are preflight-checked against the API server -- Get the named
// FunctionAlias/FunctionVersion and confirm it actually belongs to fnName
// (util.GetOwnedFunctionAlias/util.GetOwnedFunctionVersion, shared with
// `fn pods`/`fn logs`) -- so a typo surfaces here as a clear error instead of
// an opaque router 404 once the request is sent (see
// suffixedRouteNotFoundError for the 404 that preflight can't catch: a
// real-but-not-yet-resolved alias).
func (opts *TestSubCommand) resolveTestRefSuffix(input cli.Input, fnName, namespace string) (string, error) {
	aliasName := input.String(flagkey.FnTestAlias)
	versionName := input.String(flagkey.FnTestVersion)

	if err := util.MutuallyExclusive(flagkey.FnTestAlias, aliasName, flagkey.FnTestVersion, versionName); err != nil {
		return "", err
	}

	switch {
	case aliasName != "":
		if _, err := util.GetOwnedFunctionAlias(input.Context(), opts.Client(), namespace, fnName, aliasName); err != nil {
			return "", err
		}
		return aliasName, nil
	case versionName != "":
		if _, err := util.GetOwnedFunctionVersion(input.Context(), opts.Client(), namespace, fnName, versionName); err != nil {
			return "", err
		}
		return versionName, nil
	default:
		return "", nil
	}
}

// handleAsyncResponse interprets the router's response to a `fission function
// test --async` (RFC-0024) request: 202 carries the durable invocation id
// (in the X-Fission-Invocation-Id header, or the JSON body as a fallback),
// which is printed; the response body is otherwise not awaited. The body is
// capped at 8KiB since only NotImplemented/error bodies are ever read in full
// here, never a function's real payload. fnName/suffix are only used to
// upgrade a bare 404 into the RFC-0025 alias/version hint (see
// suffixedRouteNotFoundError) -- the enqueue itself already happened against
// the suffixed route baked into the request URL by the caller.
func handleAsyncResponse(resp *http.Response, fnName, suffix string) error {
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
	case http.StatusNotFound:
		if msg, ok := suffixedRouteNotFoundError(fnName, suffix, resp.Header.Get(utils.HeaderRouteMiss) != ""); ok {
			return errors.New(msg)
		}
		return fmt.Errorf("async invocation failed (%s): %s", resp.Status, strings.TrimSpace(string(body)))
	default:
		return fmt.Errorf("async invocation failed (%s): %s", resp.Status, strings.TrimSpace(string(body)))
	}
}

// suffixedRouteNotFoundError turns a router route-miss 404 into an actionable
// message when the request was for a suffixed (`:<alias>`/`:<version>`)
// route: the preflight in resolveTestRefSuffix already confirmed the
// FunctionAlias/FunctionVersion object exists and belongs to this function,
// so a route-miss here means the router hasn't materialized that route yet --
// most commonly a freshly created/repointed alias that hasn't finished its
// async resolve pass. routeMiss is whether the 404 carried the router's
// route-miss marker (utils.HeaderRouteMiss, stamped only by the internal
// listener's own not-found path and stripped from function-proxied
// responses): without it the 404 is the FUNCTION'S own response, so ok is
// false and the caller renders it as a normal function failure instead of
// wrongly blaming route materialization. ok is likewise false when suffix is
// empty, since a bare function route 404 has nothing to do with RFC-0025.
// (Version-skew note: a pre-marker router never sets the header, so against
// an older server the hint is not shown and the 404 falls through to the
// generic rendering -- less specific, never wrong.)
func suffixedRouteNotFoundError(fnName, suffix string, routeMiss bool) (string, bool) {
	if suffix == "" || !routeMiss {
		return "", false
	}
	return fmt.Sprintf("alias/version route not found for function %q at %q — check `fission alias get` / that the version exists; aliases resolve asynchronously", fnName, suffix), true
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

type invokeOptions struct {
	Method            string // raw, gets validated/normalized internally
	URL               string
	Body              string
	Headers           []string // --header flags;
	SignWithHMAC      bool     // fn test paths: true; invokeLocal: false
	AttachBearerToken bool     //invokeLocal: true; fn test paths: false
	InvokeModeHeader  string   // "" for sync, async.InvokeModeAsync for -- async; "" for invokeLocal
}

// combinedHTTPRequest builds and sends the HTTP request behind a function
// invocation, applying HMAC signing, a bearer token, and/or an invoke-mode
// header per opts. It is shared by `function test` (do, above) and `function
// run`'s local invoke (invokeLocal, in run.go) so both paths build and send
// requests identically.
func combinedHTTPRequest(ctx context.Context, opts invokeOptions) (*http.Response, error) {
	method, err := httptrigger.GetMethod(opts.Method)
	if err != nil {
		return nil, err
	}
	shutdown, err := otelUtils.InitProvider(ctx, logr.Logger{}, "fission-cli")
	if err != nil {
		console.Warn(fmt.Sprintf("otel init failed: %v", err))
	}
	if shutdown != nil {
		defer shutdown(ctx)
	}
	tracer := otel.Tracer("fission-cli")
	spanCtx, span := tracer.Start(ctx, "httpRequest")
	defer span.End()
	req, err := http.NewRequestWithContext(spanCtx, method, opts.URL, strings.NewReader(opts.Body))
	if err != nil {
		return nil, fmt.Errorf("error creating HTTP request: %w", err)
	}
	for _, header := range opts.Headers {
		headerKeyValue := strings.SplitN(header, ":", 2)
		if len(headerKeyValue) != 2 {
			return nil, errors.New("failed to create request without appropriate headers")
		}
		req.Header.Set(headerKeyValue[0], headerKeyValue[1])
	}

	if opts.AttachBearerToken {
		accessToken, ok := os.LookupEnv(util.FISSION_AUTH_TOKEN)
		if ok && len(accessToken) != 0 {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %v", accessToken))
		}
	}

	if opts.InvokeModeHeader != "" {
		req.Header.Set(asyncinvoke.HeaderInvokeMode, opts.InvokeModeHeader)
	}

	var transport http.RoundTripper = otelhttp.NewTransport(http.DefaultTransport)
	if opts.SignWithHMAC {
		secret := os.Getenv("FISSION_INTERNAL_AUTH_SECRET")
		if secret != "" {
			transport = hmacauth.NewServiceSigningTransport(
				[]byte(secret),
				hmacauth.ServiceRouterInternal,
				transport,
				"/fission-function/",
			)
		}
	}

	hc := &http.Client{Transport: transport}

	if console.Verbosity >= 2 {
		dumpReq, err := httputil.DumpRequestOut(req, false)
		if err != nil {
			return nil, err
		}
		console.Verbose(2, "%s", string(dumpReq))
	}

	resp, err := hc.Do(req)
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
