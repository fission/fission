// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
	"github.com/fission/fission/test/integration/testdata"
)

// TestSpec is the Go port of test/tests/test_specs/test_spec.sh. It exercises
// the `fission spec init / env create --spec / spec apply / fn create --spec`
// declarative workflow end to end.
//
// Implementation note: `env create --spec` and `fn create --spec` write into
// `./specs` under the *current working directory* — they don't accept
// --specdir. We use the framework's WithCWD helper to chdir into a per-test
// temp directory under a process-global mutex; concurrent non-spec tests are
// unaffected because they all pass absolute paths to the CLI.
func TestSpec(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequirePython(t)

	ns := f.NewTestNamespace(t)
	envName := "python-spec-" + ns.ID
	fnName := "spec-" + ns.ID
	routePath := "/" + fnName

	workdir := t.TempDir()
	helloBytes, err := testdata.FS.ReadFile("python/hello/hello.py")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(workdir, "hello.py"), helloBytes, 0o644))

	ns.WithCWD(t, workdir, func() {
		ns.CLI(t, ctx, "spec", "init")
		for _, p := range []string{"specs/README", "specs/fission-deployment-config.yaml"} {
			_, err := os.Stat(filepath.Join(workdir, p))
			require.NoErrorf(t, err, "spec init should have created %q", p)
		}

		ns.CLI(t, ctx, "env", "create", "--spec", "--name", envName, "--image", image)
		ns.CLI(t, ctx, "spec", "apply")
		// `env list` writes its tabular output to os.Stdout (which the
		// in-process CLI helper doesn't capture), so we don't assert on
		// it. The "X resources created" lines from spec apply are enough
		// evidence; the route+invoke below is the end-to-end check.

		ns.CLI(t, ctx, "fn", "create", "--spec", "--name", fnName, "--env", envName, "--code", "hello.py")
		_, err := os.Stat(filepath.Join(workdir, "specs", "function-"+fnName+".yaml"))
		require.NoErrorf(t, err, "fn create --spec should have written function-%s.yaml", fnName)

		ns.CLI(t, ctx, "spec", "apply")
	})

	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: fnName, URL: routePath, Method: "GET"})
	f.Router(t).GetEventually(t, ctx, routePath, framework.BodyContains("hello"))
}

// TestSpecOCIPackage covers the GitOps path for pre-built OCI packages: a
// spec authored with --oci (fully digest-pinned reference) applies without
// any archive upload, and the resulting Package carries the reference
// declaratively. No registry is needed — nothing pulls until invocation.
func TestSpecOCIPackage(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)

	envName := "go-ocispec-" + ns.ID
	fnName := "fn-ocispec-" + ns.ID
	digest := "sha256:" + strings.Repeat("a", 64)
	ref := "ghcr.io/example/pkgs/" + fnName + ":v1@" + digest

	workdir := t.TempDir()
	ns.WithCWD(t, workdir, func() {
		ns.CLI(t, ctx, "spec", "init")
		ns.CLI(t, ctx, "env", "create", "--spec", "--name", envName, "--image", "ghcr.io/fission/go-env-1.26")
		ns.CLI(t, ctx, "fn", "create", "--spec", "--name", fnName, "--env", envName,
			"--oci", ref, "--entrypoint", "Handler")
		ns.CLI(t, ctx, "spec", "apply")
	})

	fn, err := f.FissionClient().CoreV1().Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
	require.NoError(t, err)
	pkg, err := f.FissionClient().CoreV1().Packages(ns.Name).Get(ctx, fn.Spec.Package.PackageRef.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, fv1.ArchiveTypeOCI, pkg.Spec.Deployment.Type, "spec apply must preserve the OCI archive")
	require.NotNil(t, pkg.Spec.Deployment.OCI)
	require.Equal(t, "ghcr.io/example/pkgs/"+fnName+":v1", pkg.Spec.Deployment.OCI.Image,
		"the digest must be split out of the pasted reference")
	require.Equal(t, digest, pkg.Spec.Deployment.OCI.Digest, "digest-pinned")
}
