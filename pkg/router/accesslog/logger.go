package accesslog

import (
	"io"
	"net/http"
	"os"

	otelUtils "github.com/fission/fission/pkg/utils/otel"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"go.uber.org/zap"
)

func Logger(logger *zap.Logger) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return handlers.CustomLoggingHandler(os.Stdout, next, func(w io.Writer, params handlers.LogFormatterParams) {
			ctx := params.Request.Context()
			upstream := newUpstreamFrom(ctx)
			logger := otelUtils.LoggerWithTraceID(ctx, logger)
			logger.With(
				zap.String("host", params.Request.Host),
				zap.String("method", params.Request.Method),
				zap.String("uri", params.Request.RequestURI),
				zap.String("proto", params.Request.Proto),
				zap.Int("status", params.StatusCode),
				zap.Int64("request_length", params.Request.ContentLength),
				zap.Int("body_bytes_sent", params.Size),
				zap.String("upstream_addr", upstream.Addr),
				zap.Int64("upstream_response_length", upstream.ResponseLength),
				zap.Int64("upstream_response_time", upstream.ResponseTime),
				zap.Int("upstream_status", upstream.ResponseStatus),
			).Info(params.URL.Path)
		})
	}
}
