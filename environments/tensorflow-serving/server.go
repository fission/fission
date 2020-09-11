package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	PortgRPC    = 8500
	PortRestAPI = 8501
)

var (
	specialized = false

	// for tensorflow serving to use
	MODEL_NAME = ""
	API_TYPE   = ""
)

type (
	FunctionLoadRequest struct {
		// FilePath is an absolute filesystem path to the
		// function. What exactly is stored here is
		// env-specific. Optional.
		FilePath string `json:"filepath"`

		// FunctionName has an environment-specific meaning;
		// usually, it defines a function within a module
		// containing multiple functions. Optional; default is
		// environment-specific.
		FunctionName string `json:"functionName"`

		// URL to expose this function at. Optional; defaults
		// to "/".
		URL string `json:"url"`
	}
)

func specializeHandler(logger *zap.Logger) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Error("v1 interface is not implemented")
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func specializeHandlerV2(logger *zap.Logger) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if specialized {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("Not a generic container"))
			return
		}

		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			logger.Error("error reading request body", zap.Error(err))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var loadreq FunctionLoadRequest
		err = json.Unmarshal(body, &loadreq)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Tensorflow-serving supports three types of API: classify, regress, predict
		// To get the API type, we need to split the entry point with separator ":"
		// POST http://host:port/v1/models/${MODEL_NAME}:(classify|regress:predict)
		entrypoint := strings.Split(loadreq.FunctionName, ":")
		modelDir, apiType := "", ""

		if len(entrypoint) == 0 {
			logger.Error("unable to load model due to empty entrypoint")
			w.WriteHeader(http.StatusBadRequest)
			return
		} else if len(entrypoint) == 1 {
			modelDir = entrypoint[0]
			apiType = "predict" // assign default API type
		} else {
			modelDir = entrypoint[0]
			apiType = entrypoint[1]
		}

		// To ensure we load model from the expected path
		basePath := fmt.Sprintf("%v/%v", loadreq.FilePath, modelDir)
		basePath, err = filepath.Abs(basePath)
		if err != nil {
			msg := "error getting absolute path of model"
			logger.Error(msg, zap.Error(err))
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
			return
		} else if !strings.HasPrefix(basePath, loadreq.FilePath) {
			msg := "incorrect model base path"
			logger.Error(msg, zap.String("model_base_path", basePath))
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(msg))
			return
		}

		_, err = os.Stat(basePath)
		if err != nil {
			msg := "error checking model status"
			logger.Error(msg, zap.Error(err))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// get directory name that holds model
		MODEL_NAME = filepath.Base(basePath)
		API_TYPE = apiType

		argModelBasePath := fmt.Sprintf("--model_base_path=%v", basePath)
		argModelName := fmt.Sprintf("--model_name=%v", MODEL_NAME)
		argPortgRPC := fmt.Sprintf("--port=%v", PortgRPC)
		argPortREST := fmt.Sprintf("--rest_api_port=%v", PortRestAPI)

		logger.Info(fmt.Sprintf("specializing: %v %v", loadreq.FunctionName, loadreq.FilePath))

		// Future: could be improved by keeping subprocess open while environment is specialized
		cmd := exec.Command("tensorflow_model_server",
			argPortgRPC, argPortREST, argModelName, argModelBasePath)

		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		err = cmd.Start()
		if err != nil {
			msg := "error starting tensorflow serving"
			logger.Error(msg, zap.Error(err))
			err = errors.Wrap(err, msg)
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
			return
		}

		go func() {
			err = cmd.Wait()
			if err != nil {
				logger.Fatal("error running tensorflow serving", zap.Error(err))
			}
		}()

		t := time.Now()
		retryInterval := 50 * time.Millisecond

		// tensorflow serving takes some time to load model
		// into memory, keep retrying until it starts REST api server.
		for {
			if time.Since(t) > 30*time.Second {
				w.WriteHeader(http.StatusGatewayTimeout)
				return
			}
			conn, err := net.Dial("tcp", "localhost:8501")
			if err == nil {
				conn.Close()
				break
			} else {
				logger.Info(fmt.Sprintf("waiting for tensorflow serving to be ready: %v", err.Error()))
				time.Sleep(retryInterval)
				retryInterval = retryInterval * 2
			}
		}

		specialized = true

		logger.Info("done")
	}
}

func readinessProbeHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func main() {

	config := zap.NewProductionConfig()
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	logger, err := config.Build()

	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer logger.Sync()

	http.HandleFunc("/healthz", readinessProbeHandler)
	http.HandleFunc("/specialize", specializeHandler(logger.Named("specialize_handler")))
	http.HandleFunc("/v2/specialize", specializeHandlerV2(logger.Named("specialize_v2_handler")))

	// Generic route -- all http requests go to the user function.
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !specialized {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Generic container: no requests supported"))
			return
		}

		// TODO: replace it with gRPC (https://gist.github.com/mauri870/1f953a183ee6c186e70a0a72e78b088c)
		// set up proxy server director
		director := func(req *http.Request) {
			req.URL.Scheme = "http"
			req.URL.Host = "localhost:8501"
			req.URL.Path = fmt.Sprintf("/v1/models/%v:%v", MODEL_NAME, API_TYPE)
			req.Host = "localhost:8501"
		}

		proxy := &httputil.ReverseProxy{
			Director: director,
		}
		proxy.ServeHTTP(w, r)
	})

	logger.Info("listening on 8888 ...")
	http.ListenAndServe(":8888", nil)
}
