// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"time"

	"github.com/fission/fission/test/benchmark/pkg/harness"
	"github.com/fission/fission/test/benchmark/pkg/report"
)

// buildTime measures how long the builder takes to compile a source package for
// an environment (source upload -> builder pod -> deploy archive). v1 covers the
// Python builder; other languages follow the same shape once their builder
// images are wired into the run.
type buildTime struct {
	timeout time.Duration
}

func (b *buildTime) Name() string   { return "build-time-python" }
func (b *buildTime) Tags() []string { return []string{"build", "package"} }

func (b *buildTime) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	env := sc.Env()
	if env.Images.Python == "" || env.Images.PythonBuilder == "" {
		return res, skip("PYTHON_RUNTIME_IMAGE/PYTHON_BUILDER_IMAGE unset")
	}

	envName := sc.Name("build-env")
	if err := sc.CreateEnv(ctx, harness.EnvOptions{
		Name: envName, Image: env.Images.Python, Builder: env.Images.PythonBuilder, Version: 2, Poolsize: 1,
	}); err != nil {
		return res, err
	}

	srcZip, err := zipFiles(map[string]string{"hello.py": pythonHello})
	if err != nil {
		return res, err
	}
	pkgName := sc.Name("build-pkg")
	if err := sc.CreateSourcePackage(ctx, pkgName, envName, srcZip, ""); err != nil {
		return res, err
	}

	dur, err := env.WaitForPackageBuild(ctx, pkgName, b.timeout)
	if err != nil {
		return res, err
	}
	res.Add("build_seconds", "s", report.Lower, dur.Seconds())
	return res, nil
}

// zipFiles builds an in-memory zip archive from name->content.
func zipFiles(files map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, content); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
