/*
Copyright 2016 The Fission Authors.

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

package builder

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/dchest/uniuri"
	"github.com/pkg/errors"
	"go.uber.org/zap"

	"github.com/fission/fission/pkg/info"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

const (
	// supported environment variables
	envSrcPkg    string = "SRC_PKG"
	envDeployPkg string = "DEPLOY_PKG"
)

type (
	PackageBuildRequest struct {
		SrcPkgFilename string `json:"srcPkgFilename"`
		// Command for builder to run with.
		// A build command consists of commands, parameters and environment variables.
		// For now, two environment variables are supported:
		// 1. SRC_PKG: path to source package directory
		// 2. DEPLOY_PKG: path to deployment package directory
		BuildCommand string `json:"command"`
	}

	PackageBuildResponse struct {
		ArtifactFilename string `json:"artifactFilename"`
		BuildLogs        string `json:"buildLogs"`
	}

	Builder struct {
		logger           *zap.Logger
		sharedVolumePath string
	}
)

func MakeBuilder(logger *zap.Logger, sharedVolumePath string) *Builder {
	return &Builder{
		logger:           logger.Named("builder"),
		sharedVolumePath: sharedVolumePath,
	}
}

func (builder *Builder) VersionHandler(w http.ResponseWriter, r *http.Request) {
	logger := otelUtils.LoggerWithTraceID(r.Context(), builder.logger)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, err := w.Write([]byte(info.BuildInfo().String()))
	if err != nil {
		logger.Error(
			"error writing response",
			zap.Error(err),
		)
	}
}

func (builder *Builder) Handler(w http.ResponseWriter, r *http.Request) {
	logger := otelUtils.LoggerWithTraceID(r.Context(), builder.logger)

	if r.Method != "POST" {
		e := "method not allowed"
		logger.Error(e, zap.String("http_method", r.Method))
		builder.reply(r.Context(), w, "", fmt.Sprintf("%s: %s", e, r.Method), http.StatusMethodNotAllowed)
		return
	}

	startTime := time.Now()
	defer func() {
		elapsed := time.Since(startTime)
		logger.Info("build request complete", zap.Duration("elapsed_time", elapsed))
	}()

	// parse request
	body, err := io.ReadAll(r.Body)
	if err != nil {
		e := "error reading request body"
		logger.Error(e, zap.Error(err))
		builder.reply(r.Context(), w, "", fmt.Sprintf("%s: %s", e, err.Error()), http.StatusInternalServerError)
		return
	}
	var req PackageBuildRequest
	err = json.Unmarshal(body, &req)
	if err != nil {
		e := "error parsing json body"
		logger.Error(e, zap.Error(err))
		builder.reply(r.Context(), w, "", fmt.Sprintf("%s: %s", e, err.Error()), http.StatusBadRequest)
		return
	}
	logger.Info("builder received request", zap.Any("request", req))

	logger.Debug("starting build")
	srcPkgPath := filepath.Join(builder.sharedVolumePath, req.SrcPkgFilename)
	deployPkgFilename := fmt.Sprintf("%s-%s", req.SrcPkgFilename, strings.ToLower(uniuri.NewLen(6)))
	deployPkgPath := filepath.Join(builder.sharedVolumePath, deployPkgFilename)

	var buildArgs []string
	buildCmd := req.BuildCommand
	if len(buildCmd) == 0 {
		// use default build command
		buildCmd = "/build"
	} else {
		// split executable command and arguments
		args := strings.Split(buildCmd, " ")
		buildCmd = args[0] // get the executable command, executable command will always be on Zero index

		// get all the arguments
		for i := 1; i < len(args); i++ {
			buildArgs = append(buildArgs, args[i])
		}
	}
	buildLogs, err := builder.build(r.Context(), buildCmd, buildArgs, srcPkgPath, deployPkgPath)
	if err != nil {
		e := "error building source package"
		logger.Error(e, zap.Error(err))

		// append error at the end of build logs
		buildLogs += fmt.Sprintf("%s: %s\n", e, err.Error())
		builder.reply(r.Context(), w, deployPkgFilename, buildLogs, http.StatusInternalServerError)
		return
	}

	builder.reply(r.Context(), w, deployPkgFilename, buildLogs, http.StatusOK)
}

func (builder *Builder) reply(ctx context.Context, w http.ResponseWriter, pkgFilename string, buildLogs string, statusCode int) {
	logger := otelUtils.LoggerWithTraceID(ctx, builder.logger)
	resp := PackageBuildResponse{
		ArtifactFilename: pkgFilename,
		BuildLogs:        buildLogs,
	}

	rBody, err := json.Marshal(resp)
	if err != nil {
		e := errors.Wrap(err, "error encoding response body")
		rBody = []byte(fmt.Sprintf(`{"buildLogs": "%s"}`, e.Error()))
		statusCode = http.StatusInternalServerError
	}

	w.Header().Set("Content-Type", "application/json")
	// should write header before writing the body,
	// or client will receive HTTP 200 regardless the real status code
	w.WriteHeader(statusCode)
	_, err = w.Write(rBody)
	if err != nil {
		logger.Error(
			"error writing response",
			zap.Error(err),
		)
	}
}

func (builder *Builder) build(ctx context.Context, command string, args []string, srcPkgPath string, deployPkgPath string) (string, error) {
	logger := otelUtils.LoggerWithTraceID(ctx, builder.logger)

	cmd := exec.Command(command, args...)

	fi, err := os.Stat(srcPkgPath)
	if err != nil {
		return "", fmt.Errorf("could not find srcPkgPath: '%s'", srcPkgPath)
	}
	if fi.IsDir() {
		cmd.Dir = srcPkgPath
	} else {
		cmd.Dir = path.Dir(srcPkgPath)
	}

	// set env variables for build command
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("%s=%s", envSrcPkg, srcPkgPath),
		fmt.Sprintf("%s=%s", envDeployPkg, deployPkgPath),
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", errors.Wrap(err, "error creating stdout pipe for cmd")
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", errors.Wrap(err, "error creating stderr pipe for cmd")
	}

	// Init logs
	logger.Info("building source package", zap.String("command", command), zap.Strings("args", args), zap.Strings("env", cmd.Env))

	out := io.MultiReader(stdout, stderr)
	scanner := bufio.NewScanner(out)

	err = cmd.Start()
	if err != nil {
		return "", errors.Wrap(err, "error starting cmd")
	}
	fmt.Printf("========= START =========\n")
	defer fmt.Printf("========= END ===========\n")
	var buildLogs string
	// Runtime logs
	for scanner.Scan() {
		output := scanner.Text()
		fmt.Println(output)
		buildLogs += fmt.Sprintf("%s\n", output)
	}

	if err := scanner.Err(); err != nil {
		scanErr := errors.Wrap(err, "error reading cmd output")
		fmt.Println(scanErr)
		return buildLogs, scanErr
	}

	err = cmd.Wait()
	if err != nil {
		cmdErr := errors.Wrapf(err, "error waiting for cmd %q", command)
		fmt.Println(cmdErr)
		return buildLogs, cmdErr
	}
	return buildLogs, nil
}
