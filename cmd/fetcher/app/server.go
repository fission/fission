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
	"sync/atomic"

	"go.opencensus.io/plugin/ochttp"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"

	"github.com/fission/fission/pkg/fetcher"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
	"github.com/fission/fission/pkg/utils/tracing"
)

var (
	readyToServe uint32
)

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

	ctx := context.Background()
	openTracingEnabled := tracing.TracingEnabled(logger)
	if openTracingEnabled {
		go func() {
			if err := tracing.RegisterTraceExporter(logger, *collectorEndpoint, "Fission-Fetcher"); err != nil {
				logger.Fatal("could not register trace exporter", zap.Error(err), zap.String("collector_endpoint", *collectorEndpoint))
			}
		}()
	} else {
		shutdown, err := otelUtils.InitProvider(ctx, logger, "Fission-Fetcher")
		if err != nil {
			logger.Fatal("error initializing provider for OTLP", zap.Error(err))
		}
		if shutdown != nil {
			defer shutdown(ctx)
		}
	}

	tracer := otel.Tracer("fetcher")
	ctx, span := tracer.Start(ctx, "fetcher/Run")
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
		handler = otelUtils.GetHandlerWithOTEL(mux, "fission-fetcher", otelUtils.UrlsToIgnore("/healthz", "/readiness-healthz"))
	}
	if err = http.ListenAndServe(":8000", handler); err != nil {
		log.Fatal(err)
	}
}

func fetcherUsage() {
	fmt.Println("Usage: fetcher [-specialize-on-startup] [-specialize-request <json>] [-secret-dir <string>] [-cfgmap-dir <string>] <shared volume path>")
}
