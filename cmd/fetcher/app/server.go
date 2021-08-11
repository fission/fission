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

package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"

	"contrib.go.opencensus.io/exporter/jaeger"
	"go.opencensus.io/plugin/ochttp"
	"go.opencensus.io/trace"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/fission/fission/pkg/fetcher"
)

var (
	readyToServe uint32
)

func registerTraceExporter(collectorEndpoint string) error {
	if collectorEndpoint == "" {
		return nil
	}

	serviceName := "Fission-Fetcher"
	exporter, err := jaeger.NewExporter(jaeger.Options{
		CollectorEndpoint: collectorEndpoint,
		Process: jaeger.Process{
			ServiceName: serviceName,
			Tags: []jaeger.Tag{
				jaeger.BoolTag("fission", true),
			},
		},
	})
	if err != nil {
		return err
	}
	trace.RegisterExporter(exporter)
	trace.ApplyConfig(trace.Config{DefaultSampler: trace.AlwaysSample()})
	return nil
}

// Initializes an OTLP exporter, and configures the corresponding trace and metric providers.
func initProvider(logger *zap.Logger) (func(), error) {
	collectorEndpoint := os.Getenv("OTEL_COLLECTOR_ENDPOINT")
	if collectorEndpoint == "" {
		logger.Info("skipping trace exporter registration")
		return nil, nil
	}
	ctx := context.Background()
	const serviceName = "Fission-Fetcher"

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
		),
	)
	if err != nil {
		return nil, err
	}

	traceExporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(collectorEndpoint),
		otlptracegrpc.WithDialOption(grpc.WithBlock()),
	)
	if err != nil {
		return nil, err
	}

	bsp := sdktrace.NewBatchSpanProcessor(traceExporter)
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(bsp),
	)

	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Shutdown will flush any remaining spans and shut down the exporter.
	return func() {
		err := tracerProvider.Shutdown(ctx)
		if err != nil {
			logger.Fatal("error shutting down trace provider", zap.Error(err))
		}
	}, nil
}

func Run(logger *zap.Logger) {
	flag.Usage = fetcherUsage
	collectorEndpoint := flag.String("jaeger-collector-endpoint", "", "")
	specializeOnStart := flag.Bool("specialize-on-startup", false, "Flag to activate specialize process at pod starup")
	specializePayload := flag.String("specialize-request", "", "JSON payload for specialize request")
	secretDir := flag.String("secret-dir", "", "Path to shared secrets directory")
	configDir := flag.String("cfgmap-dir", "", "Path to shared configmap directory")

	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}

	dir := flag.Arg(0)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			err = os.MkdirAll(dir, os.ModeDir|0700)
			if err != nil {
				logger.Fatal("error creating directory", zap.Error(err), zap.String("directory", dir))
			}
		}
	}

	openTracingEnabled, err := strconv.ParseBool(os.Getenv("OPENTRACING_ENABLED"))
	if err != nil {
		logger.Fatal("error parsing OPENTRACING_ENABLED", zap.Error(err))
	}

	var shutdown func()
	if openTracingEnabled {
		go func() {
			if err := registerTraceExporter(*collectorEndpoint); err != nil {
				logger.Fatal("could not register trace exporter", zap.Error(err), zap.String("collector_endpoint", *collectorEndpoint))
			}
		}()
	} else {
		shutdown, err = initProvider(logger)
		if err != nil {
			logger.Fatal("error initializing provider for OTLP", zap.Error(err))
		}
	}
	if shutdown != nil {
		defer shutdown()
	}

	tracer := otel.Tracer("fetcher")
	ctx, span := tracer.Start(context.Background(), "fetcher/Run")
	defer span.End()

	f, err := fetcher.MakeFetcher(logger, dir, *secretDir, *configDir)
	if err != nil {
		logger.Fatal("error making fetcher", zap.Error(err))
	}

	// do specialization in other goroutine to prevent blocking in newdeploy
	go func() {
		if *specializeOnStart {
			var specializeReq fetcher.FunctionSpecializeRequest

			err := json.Unmarshal([]byte(*specializePayload), &specializeReq)
			if err != nil {
				logger.Fatal("error decoding specialize request", zap.Error(err))
			}

			err = f.SpecializePod(ctx, specializeReq.FetchReq, specializeReq.LoadReq)
			if err != nil {
				logger.Fatal("error specializing function pod", zap.Error(err))
			}
		}
		atomic.StoreUint32(&readyToServe, 1)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/fetch", f.FetchHandler)
	mux.HandleFunc("/specialize", f.SpecializeHandler)
	mux.HandleFunc("/upload", f.UploadHandler)
	mux.HandleFunc("/version", f.VersionHandler)
	mux.HandleFunc("/wsevent/start", f.WsStartHandler)
	mux.HandleFunc("/wsevent/end", f.WsEndHandler)

	readinessHandler := func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadUint32(&readyToServe) == 1 {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}

	mux.HandleFunc("/readiness-healthz", readinessHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	logger.Info("fetcher ready to receive requests")

	var handler http.Handler
	if openTracingEnabled {
		handler = &ochttp.Handler{Handler: mux}
	} else {
		handler = otelhttp.NewHandler(mux, "fission-fetcher",
			otelhttp.WithMessageEvents(otelhttp.ReadEvents, otelhttp.WriteEvents),
		)
	}
	if err = http.ListenAndServe(":8000", handler); err != nil {
		log.Fatal(err)
	}
}

func fetcherUsage() {
	fmt.Println("Usage: fetcher [-specialize-on-startup] [-specialize-request <json>] [-secret-dir <string>] [-cfgmap-dir <string>] <shared volume path>")
}
