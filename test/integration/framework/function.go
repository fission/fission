//go:build integration

package framework

import (
	"context"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FunctionOptions are the inputs to TestNamespace.CreateFunction.
//
// Either Code (single file) or Src (zipped source package) must be provided,
// not both. When Src is provided, Entrypoint and BuildCmd describe how the
// builder should produce the deploy archive.
type FunctionOptions struct {
	// Name of the Function CR. Required.
	Name string
	// Env is the Environment name to use. Required.
	Env string
	// Code is the local file path to a single source file (e.g. hello.js).
	Code string
	// Src is the local file path to a source archive (e.g. .zip). The
	// environment must have a Builder configured.
	Src string
	// Entrypoint identifies the function in the source archive (e.g. "user.main").
	Entrypoint string
	// BuildCmd is the command run by the builder (e.g. "./build.sh").
	BuildCmd string
}

// CreateFunction creates a Function via the CLI from either Code or Src. The
// CLI also creates a backing Package with a generated name (`<fn>-<uuid>`).
// Cleanup deletes both: the Function explicitly and any Package whose name
// starts with the function name.
func (ns *TestNamespace) CreateFunction(t *testing.T, ctx context.Context, opts FunctionOptions) {
	t.Helper()
	if opts.Name == "" || opts.Env == "" {
		t.Fatalf("CreateFunction: Name and Env are required (got %+v)", opts)
	}
	if (opts.Code == "") == (opts.Src == "") {
		t.Fatalf("CreateFunction: exactly one of Code or Src must be set (got %+v)", opts)
	}
	args := []string{"fn", "create", "--name", opts.Name, "--env", opts.Env}
	if opts.Code != "" {
		args = append(args, "--code", opts.Code)
	} else {
		args = append(args, "--src", opts.Src)
		if opts.Entrypoint != "" {
			args = append(args, "--entrypoint", opts.Entrypoint)
		}
		if opts.BuildCmd != "" {
			args = append(args, "--buildcmd", opts.BuildCmd)
		}
	}
	ns.CLI(t, ctx, args...)

	ns.addCleanup("function "+opts.Name, func(c context.Context) error {
		fc := ns.f.fissionClient.CoreV1()
		if err := fc.Functions(ns.Name).Delete(c, opts.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		// The CLI auto-generates a Package named `<fn>-<uuid>`. Find and delete any.
		pkgs, err := fc.Packages(ns.Name).List(c, metav1.ListOptions{})
		if err != nil {
			return err
		}
		for _, p := range pkgs.Items {
			if strings.HasPrefix(p.Name, opts.Name+"-") {
				if delErr := fc.Packages(ns.Name).Delete(c, p.Name, metav1.DeleteOptions{}); delErr != nil && !apierrors.IsNotFound(delErr) {
					return delErr
				}
			}
		}
		return nil
	})
}

// WaitForFunction polls until the Function CR exists in the test namespace, or
// the context times out. Use this when the CLI returns before the controller
// has processed the request.
func (ns *TestNamespace) WaitForFunction(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	Eventually(t, ctx, 30*time.Second, 500*time.Millisecond, func(c context.Context) (bool, error) {
		_, err := ns.f.fissionClient.CoreV1().Functions(ns.Name).Get(c, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return err == nil, err
	}, "function %q not visible in namespace %q", name, ns.Name)
}

// FunctionPackageName returns the auto-generated Package name backing a
// Function (Spec.Package.PackageRef.Name). Mirrors the bash one-liner:
// `kubectl get functions <fn> -o jsonpath='{.spec.package.packageref.name}'`.
//
// `fission fn update --src` patches the existing Package in place, so this
// name is *stable* across rebuilds — use Package.Status.LastUpdateTimestamp
// (see WaitForPackageRebuiltSince) to detect a rebuild instead.
func (ns *TestNamespace) FunctionPackageName(t *testing.T, ctx context.Context, fnName string) string {
	t.Helper()
	fn, err := ns.f.fissionClient.CoreV1().Functions(ns.Name).Get(ctx, fnName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("FunctionPackageName: get function %q: %v", fnName, err)
	}
	if fn.Spec.Package.PackageRef.Name == "" {
		t.Fatalf("FunctionPackageName: function %q has no package reference yet", fnName)
	}
	return fn.Spec.Package.PackageRef.Name
}
