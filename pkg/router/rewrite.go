// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils"
)

const (
	// FORWARDED represents the 'Forwarded' request header
	FORWARDED = "Forwarded"

	// X_FORWARDED_HOST represents the 'X_FORWARDED_HOST' request header
	X_FORWARDED_HOST = "X-Forwarded-Host"
)

// functionURLBases returns the internal-listener URL prefixes
// trimFunctionPrefix tries for fnMeta — the folded default-namespace form,
// plus, for the default namespace, the qualified form too (a materialized
// `:<alias>`/`:<version>` route registers both; a plain function route only
// ever arrives via the folded one, so the extra candidate is simply never
// matched for it). See pkg/router/routeshape.go's internalRouteExactURLs for
// the registration side these mirror.
//
// This is hoisted out of the request path (RFC-0014-style): newFunctionHandlerBase
// precomputes one []string per backend function at route-build time
// (functionHandler.basesByUID) instead of every request re-deriving it from
// fnMeta and re-formatting the qualified-form string.
func functionURLBases(fnMeta *metav1.ObjectMeta) []string {
	bases := []string{utils.UrlForFunction(fnMeta.Name, fnMeta.Namespace)}
	if fnMeta.Namespace == metav1.NamespaceDefault {
		bases = append(bases, fmt.Sprintf("/fission-function/%s/%s", fnMeta.Namespace, fnMeta.Name))
	}
	return bases
}

// precomputeFunctionURLBases computes functionURLBases for every backend
// function in fns, keyed by crd.CacheKeyUG the same way precomputePolicies
// keys its map — so functionHandler.basesFor can look a route's per-request
// backend pick (canary/weighted alias) up by (UID, Generation) without
// recomputing the candidate list.
func precomputeFunctionURLBases(fns map[string]*fv1.Function) map[crd.CacheKeyUG][]string {
	bases := make(map[crd.CacheKeyUG][]string, len(fns))
	for _, fn := range fns {
		if fn == nil {
			continue
		}
		bases[crd.CacheKeyUGFromMeta(&fn.ObjectMeta)] = functionURLBases(&fn.ObjectMeta)
	}
	return bases
}

// trimFunctionPrefix strips the "/fission-function/[<ns>/]<name>[:<suffix>]"
// prefix a direct internal invocation's path carries, leaving the
// pod-visible path a plain invocation would see: "" (caller normalizes to
// "/") or "/<subpath>". ok is false when path does not carry ANY of the
// candidate bases.
//
// bases is the backend function's functionURLBases result (hoisted to
// route-build time by the caller — see functionURLBases and
// functionHandler.basesFor) — so the caller need not know which grammar form
// the request actually used.
//
// A plain strings.HasPrefix substring check is not enough here: it would
// treat "/fission-function/hello" as a prefix of BOTH "/fission-function/
// helloworld" (a different function) and "/fission-function/hello:prod"
// (leaving a garbage "/:prod" leading segment instead of consuming the whole
// tag) — the RFC-0025 bug this fixes. This checks what immediately follows
// the matched base: end-of-path or "/" (a plain invocation, unchanged
// behavior), or ":" (a tag) followed by everything up to the next "/" or
// end-of-path — the WHOLE tag is consumed, not just the base, so a suffixed
// route's pod-visible path is byte-identical to a plain invocation's.
func trimFunctionPrefix(path string, bases []string) (trimmed string, ok bool) {
	for _, base := range bases {
		rest, hasBase := strings.CutPrefix(path, base)
		if !hasBase {
			continue
		}
		switch {
		case rest == "" || strings.HasPrefix(rest, "/"):
			return rest, true
		case strings.HasPrefix(rest, ":"):
			if idx := strings.IndexByte(rest, '/'); idx >= 0 {
				return rest[idx:], true
			}
			return "", true
		}
		// rest starts with neither "/" nor ":" (e.g. base "hello" matched
		// inside "helloworld"): a false-positive substring match — keep
		// trying the remaining candidate bases.
	}
	return "", false
}

// rewriteFunctionURL points the request at the resolved service URL and
// rewrites its path per the HTTPTrigger specification:
//  1. if the trigger declares a prefix, we trim it (unless KeepPrefix) and
//     forward the request;
//  2. otherwise, if the path carries the internal-listener
//     /fission-function/[<ns>/]<name>[:<suffix>] form, that whole prefix
//     (including any `:<alias>`/`:<version>` tag) is trimmed — see
//     trimFunctionPrefix;
//  3. otherwise the request is forwarded to the root path.
//
// The query string (req.URL.RawQuery) is left intact; only req.URL.Path is
// manipulated. The request host is overwritten with the service host, or the
// request would be blocked in some situations (e.g. istio-proxy).
//
// bases is the backend function's hoisted functionURLBases result (see
// functionHandler.basesFor) — the caller resolves it once per request from
// the already-hoisted per-route map rather than this function deriving it
// from fnMeta itself.
func rewriteFunctionURL(logger logr.Logger, req *http.Request, trigger *fv1.HTTPTrigger, bases []string, serviceURL *url.URL) {
	// modify the request to reflect the service url
	// this service url comes from executor response
	req.URL.Scheme = serviceURL.Scheme
	req.URL.Host = serviceURL.Host

	prefixTrim := ""
	keepPrefix := false
	switch {
	case trigger != nil && trigger.Spec.Prefix != nil && *trigger.Spec.Prefix != "":
		prefixTrim = *trigger.Spec.Prefix
		keepPrefix = trigger.Spec.KeepPrefix
		if !keepPrefix {
			req.URL.Path = strings.TrimPrefix(req.URL.Path, prefixTrim)
		}
		if !strings.HasPrefix(req.URL.Path, "/") {
			req.URL.Path = "/" + req.URL.Path
		}
	default:
		if trimmed, ok := trimFunctionPrefix(req.URL.Path, bases); ok {
			prefixTrim = req.URL.Path[:len(req.URL.Path)-len(trimmed)]
			req.URL.Path = trimmed
			if req.URL.Path == "" {
				req.URL.Path = "/"
			}
		} else {
			req.URL.Path = "/"
		}
	}

	logger.V(1).Info("function invoke url",
		"prefixTrim", prefixTrim,
		"keepPrefix", keepPrefix,
		"hitURL", req.URL.Path)

	req.Host = serviceURL.Host
}

// addForwardedHostHeader add "forwarded host" to request header
// (see https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Forwarded).
// It runs on every proxied request, so the hostname is extracted with
// net.SplitHostPort instead of a URL parse — the previous implementation
// built "<req.Proto>://<req.Host>" ("HTTP/1.1" is not a scheme), which
// url.Parse silently read as a host-less URL: every request paid the parse,
// Hostname() was always empty, and the IPv6 quoting below never fired.
func addForwardedHostHeader(req *http.Request) {
	if len(req.Header.Get(FORWARDED)) > 0 || len(req.Header.Get(X_FORWARDED_HOST)) > 0 {
		// forwarded headers were set by external proxy, leave them intact
		return
	}

	// req.Host is <host>[:<port>]. SplitHostPort strips the port and IPv6
	// brackets; a host without a port fails the split and is used as-is
	// (a bracketed port-less IPv6 literal like "[::1]" still matches the
	// quoting test below through its brackets/colons).
	hostname := req.Host
	if h, _, err := net.SplitHostPort(req.Host); err == nil {
		hostname = h
	}

	// Per RFC 7239 a node identifier that isn't a plain token — anything
	// carrying ':' or brackets, i.e. every IPv6 textual form including
	// IPv4-mapped ("::ffff:1.2.3.4") — must be quoted. Keying on the
	// characters rather than net.ParseIP classification covers the mapped
	// forms (whose To4() is non-nil despite the colons) and skips a parse.
	// FQDNs and IPv4 stay unquoted.
	var host string
	if strings.ContainsRune(hostname, ':') || strings.HasPrefix(req.Host, "[") {
		host = `host="` + req.Host + `";`
	} else {
		host = "host=" + req.Host + ";"
	}

	req.Header.Set(FORWARDED, host)
	req.Header.Set(X_FORWARDED_HOST, req.Host)
}
