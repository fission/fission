package otel

import (
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func UrlsToIgnore(ignoreEndpoints ...string) func(r *http.Request) bool {
	return func(r *http.Request) bool {
		for _, ignore := range ignoreEndpoints {
			if strings.HasPrefix(r.URL.Path, ignore) {
				return false
			}
		}
		return true
	}
}

func GetHandlerWithOTEL(h http.Handler, name string, filter ...otelhttp.Filter) http.Handler {
	opts := []otelhttp.Option{
		otelhttp.WithMessageEvents(otelhttp.ReadEvents, otelhttp.WriteEvents),
	}

	for _, f := range filter {
		opts = append(opts, otelhttp.WithFilter(f))
	}

	return otelhttp.NewHandler(h, name, opts...)
}
