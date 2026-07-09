// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package router

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
)

const (
	// FORWARDED represents the 'Forwarded' request header
	FORWARDED = "Forwarded"

	// X_FORWARDED_HOST represents the 'X_FORWARDED_HOST' request header
	X_FORWARDED_HOST = "X-Forwarded-Host"
)

// rewriteFunctionURL points the request at the resolved service URL and
// rewrites its path per the HTTPTrigger specification:
//  1. if the trigger declares a prefix, we trim it (unless KeepPrefix) and
//     forward the request;
//  2. otherwise, if the path carries the /fission-function/<ns>/<name> form
//     (default namespace folded), that prefix is trimmed;
//  3. otherwise the request is forwarded to the root path.
//
// The query string (req.URL.RawQuery) is left intact; only req.URL.Path is
// manipulated. The request host is overwritten with the service host, or the
// request would be blocked in some situations (e.g. istio-proxy).
func rewriteFunctionURL(logger logr.Logger, req *http.Request, trigger *fv1.HTTPTrigger, fnMeta *metav1.ObjectMeta, serviceURL *url.URL) {
	// modify the request to reflect the service url
	// this service url comes from executor response
	req.URL.Scheme = serviceURL.Scheme
	req.URL.Host = serviceURL.Host

	prefixTrim := ""
	functionURL := utils.UrlForFunction(fnMeta.Name, fnMeta.Namespace)
	keepPrefix := false
	if trigger != nil && trigger.Spec.Prefix != nil && *trigger.Spec.Prefix != "" {
		prefixTrim = *trigger.Spec.Prefix
		keepPrefix = trigger.Spec.KeepPrefix
	} else if strings.HasPrefix(req.URL.Path, functionURL) {
		prefixTrim = functionURL
	}
	if prefixTrim != "" {
		if !keepPrefix {
			req.URL.Path = strings.TrimPrefix(req.URL.Path, prefixTrim)
		}
		if !strings.HasPrefix(req.URL.Path, "/") {
			req.URL.Path = "/" + req.URL.Path
		}
	} else {
		req.URL.Path = "/"
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
	// brackets; a host without a port fails the split and is used as-is,
	// except a bracketed port-less IPv6 literal ("[::1]"), whose brackets
	// must come off for ParseIP below.
	hostname := req.Host
	if h, _, err := net.SplitHostPort(req.Host); err == nil {
		hostname = h
	} else if strings.HasPrefix(hostname, "[") && strings.HasSuffix(hostname, "]") {
		hostname = hostname[1 : len(hostname)-1]
	}

	// Per RFC 7239 an IPv6 node identifier must be quoted (it contains
	// colons). The order of To4() and To16() matters: To16() also converts an
	// IPv4 address, so check To4() first. FQDNs (ParseIP nil) and IPv4 stay
	// unquoted.
	host := "host=" + req.Host + ";"
	if ip := net.ParseIP(hostname); ip != nil && ip.To4() == nil && ip.To16() != nil {
		host = `host="` + req.Host + `";`
	}

	req.Header.Set(FORWARDED, host)
	req.Header.Set(X_FORWARDED_HOST, req.Host)
}
