package controller

import (
	"net/http"
	"net/url"
	"fmt"
	"net/http/httputil"
	"strings"
)

func (api *API) WorkflowApiProxy(w http.ResponseWriter, r *http.Request) {
	u := api.storageServiceUrl
	ssUrl, err := url.Parse(u)
	if err != nil {
		msg := fmt.Sprintf("Error parsing url %v: %v", u, err)
		http.Error(w, msg, 500)
		return
	}
	director := func(req *http.Request) {
		req.URL.Scheme = ssUrl.Scheme
		req.URL.Host = ssUrl.Host
		req.URL.Path = strings.TrimPrefix(ssUrl.Path, "/proxy/workflow")
		req.URL.RawQuery = ssUrl.RawQuery
	}
	proxy := &httputil.ReverseProxy{
		Director: director,
	}
	proxy.ServeHTTP(w, r)
}
