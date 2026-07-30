// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/versioning"
)

type PublishSubCommand struct {
	cmd.CommandActioner
	function *fv1.Function
}

// Publish mints (or, if the live spec is unchanged, idempotently returns) the
// next FunctionVersion snapshot of a Function via versioning.Publish
// (pkg/versioning/publish.go).
func Publish(input cli.Input) error {
	return (&PublishSubCommand{}).do(input)
}

func (opts *PublishSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *PublishSubCommand) complete(input cli.Input) error {
	fnName := input.String(flagkey.FnName)
	_, ns, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error publishing function version: %w", err)
	}

	fn, err := opts.Client().FissionClientSet.CoreV1().Functions(ns).Get(input.Context(), fnName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read function '%v': %w", fnName, err)
	}
	opts.function = fn

	return nil
}

func (opts *PublishSubCommand) run(input cli.Input) error {
	cl := opts.Client().FissionClientSet

	// versioning.Publish itself checks package readiness and fails fast with
	// ErrPackageNotReady; --wait polls the referenced package to a build-ready
	// terminal state BEFORE calling Publish, so a build still in flight (e.g.
	// right after `fn update` kicked off a builder run) doesn't need a retry loop
	// at the call site.
	if input.Bool(flagkey.PublishWait) {
		pkgRef := opts.function.Spec.Package.PackageRef
		timeout := input.Duration(flagkey.WaitTimeout)
		if err := waitForPackageBuild(input.Context(), cl, pkgRef.Namespace, pkgRef.Name, timeout); err != nil {
			return fmt.Errorf("error waiting for package build: %w", err)
		}
	}

	result, err := versioning.Publish(input.Context(), cl, opts.function, input.String(flagkey.PublishDescription))
	if err != nil {
		if errors.Is(err, versioning.ErrPackageNotReady) {
			return fmt.Errorf("%w (retry with --wait to poll until the build finishes)", err)
		}
		return fmt.Errorf("error publishing function version: %w", err)
	}

	return printPublishResult(input.Stdout(), result, input.String(flagkey.Output))
}

// waitForPackageBuild polls the Package at namespace/name until its
// BuildStatus reaches a build-ready terminal state — BuildStatusSucceeded or
// BuildStatusNone, the same readiness predicate versioning.Publish itself
// checks — or timeout elapses. A NotFound get keeps polling (the package may
// not have been created yet by a racing builder step); BuildStatusFailed
// returns immediately since waiting longer cannot help. timeout<=0 falls back
// to util.DefaultWaitTimeout, mirroring util.RunWait. The poll scaffold
// itself is util.PollUntil, shared with util.WaitForCondition and
// functionalias.waitForResolved.
func waitForPackageBuild(ctx context.Context, cl versioned.Interface, namespace, name string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = util.DefaultWaitTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	check := func(ctx context.Context) (bool, error) {
		pkg, err := cl.CoreV1().Packages(namespace).Get(ctx, name, metav1.GetOptions{})
		switch {
		case err == nil:
			switch pkg.Status.BuildStatus {
			case fv1.BuildStatusSucceeded, fv1.BuildStatusNone:
				return true, nil
			case fv1.BuildStatusFailed:
				return false, fmt.Errorf("package %s/%s build failed", namespace, name)
			}
		case !util.IsNotFound(err):
			return false, err
		}
		return false, nil
	}

	if err := util.PollUntil(ctx, time.Second, check); err != nil {
		if util.PollEnded(err) {
			return fmt.Errorf("%s waiting for package %s/%s build to finish: %w", util.PollDeadlineVerb(ctx), namespace, name, ctx.Err())
		}
		return err
	}
	return nil
}

// printPublishResult renders a versioning.PublishResult per the --output
// value. "name" prints the bare FunctionVersion name — handled locally since
// util.ParseOutputFormat doesn't recognize it, mirroring kubectl's
// `-o name` for scripting. json/yaml marshal the FunctionVersion via
// util.PrintStructured (which, like every other structured printer in this
// package, writes straight to os.Stdout rather than w). The default table
// format's first line is the machine-readable "created <name>" (Publish
// minted a new version) or "unchanged <name>" (it idempotently returned the
// existing newest one) — this exact line is a stable contract other tooling
// greps for, so it is never altered by anything below it. A second,
// human-facing breadcrumb line follows, pointing at the next natural step:
// pointing a FunctionAlias at the version just published.
func printPublishResult(w io.Writer, result *versioning.PublishResult, outStr string) error {
	if outStr == "name" {
		_, err := fmt.Fprintln(w, result.Version.Name)
		return err
	}

	format, err := util.ParseOutputFormat(outStr)
	if err != nil {
		return err
	}
	if handled, err := util.PrintStructured(format, result.Version); err != nil || handled {
		return err
	}

	verb := "unchanged"
	if result.Created {
		verb = "created"
	}
	if _, err := fmt.Fprintf(w, "%s %s\n", verb, result.Version.Name); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "next: fission alias create --function %s --name <alias> --version %s\n",
		result.Version.Spec.FunctionName, result.Version.Name)
	return err
}
