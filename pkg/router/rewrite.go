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
func addForwardedHostHeader(logger logr.Logger, req *http.Request) {
	// for more detailed information, please visit:
	// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Forwarded

	if len(req.Header.Get(FORWARDED)) > 0 || len(req.Header.Get(X_FORWARDED_HOST)) > 0 {
		// forwarded headers were set by external proxy, leave them intact
		return
	}

	// Format of req.Host is <host>:<port>
	// We need to extract hostname from it, than
	// check whether a host is ipv4 or ipv6 or FQDN
	reqURL := fmt.Sprintf("%s://%s", req.Proto, req.Host)
	u, err := url.Parse(reqURL)
	if err != nil {
		logger.Error(err, "error parsing request url while adding forwarded host headers", "url", reqURL)
		return
	}

	var host string

	// ip will be nil if the Hostname is a FQDN string
	ip := net.ParseIP(u.Hostname())

	// ip == nil -> hostname is FQDN instead of ip address
	// The order of To4() and To16() here matters, To16() will
	// converts an IPv4 address to IPv6 format address and may
	// cause router append wrong host value to header. To prevent
	// this we need to check whether To4() is nil first.
	if ip == nil || (ip != nil && ip.To4() != nil) {
		host = fmt.Sprintf(`host=%s;`, req.Host)
	} else if ip != nil && ip.To16() != nil {
		// For the "Forwarded" header, if a host is an IPv6 address it should be quoted
		host = fmt.Sprintf(`host="%s";`, req.Host)
	}

	req.Header.Set(FORWARDED, host)
	req.Header.Set(X_FORWARDED_HOST, req.Host)
}
