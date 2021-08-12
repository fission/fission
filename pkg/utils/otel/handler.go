package otel

import (
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func GetHandlerWithOTEL(h http.Handler, name string, filter ...string) http.Handler {
	opts := []otelhttp.Option{
		otelhttp.WithMessageEvents(otelhttp.ReadEvents, otelhttp.WriteEvents),
	}

	for _, f := range filter {
		op := otelhttp.WithFilter(func(r *http.Request) bool {
			return !(strings.Compare(r.URL.Path, f) == 0)
		})
		opts = append(opts, op)
	}

	return otelhttp.NewHandler(h, name, opts...)
}
