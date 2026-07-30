// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"context"
	"fmt"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type GetSubCommand struct {
	cmd.CommandActioner
}

func Get(input cli.Input) error {
	return (&GetSubCommand{}).do(input)
}

func (opts *GetSubCommand) do(input cli.Input) error {
	fnName := input.String(flagkey.FnName)
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in get function : %w", err)
	}

	// RFC-0025: --version renders a pinned FunctionVersion's SNAPSHOT source
	// instead of the live function's, mirroring `fn describe --version`
	// (describe.go)'s preflight and split.
	if versionName := input.String(flagkey.FnTestVersion); versionName != "" {
		return opts.getVersion(input.Context(), namespace, fnName, versionName)
	}

	fn, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).Get(input.Context(), fnName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting function: %w", err)
	}

	pkg, err := opts.Client().FissionClientSet.CoreV1().Packages(fn.Spec.Package.PackageRef.Namespace).Get(input.Context(), fn.Spec.Package.PackageRef.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting package: %w", err)
	}

	os.Stdout.Write(pkg.Spec.Deployment.Literal)

	return nil
}

// getVersion renders the RFC-0025 SNAPSHOT source for one pinned
// FunctionVersion instead of the live function's package. Preflight
// (existence + Spec.FunctionName == fnName) is the shared
// util.GetOwnedFunctionVersion helper `fn test`/`fn describe`/`fn pods`/
// `fn logs` already use for their own --version flags, so a typo'd --version
// surfaces the same clear error here rather than an opaque downstream 404.
//
// version.Spec.Snapshot.Package.PackageRef is fetched directly rather than
// re-reading the live Function's package reference. What that ref names
// depends on how the version was published (versioning.Publish,
// pkg/versioning/publish.go):
//   - Legacy (non-OCI-digest) packages are mutable in place, so Publish
//     repoints the snapshot's PackageRef at a version-owned copy
//     (`<fn>-v<seq>-pkg`) created at publish time; fetching that name
//     already returns the immutable content this version was published
//     with, even if the live package has since changed.
//   - OCI-digest-backed packages are never repointed: the ref still names
//     the ORIGINAL package. That is correct, not a gap — an OCI digest
//     content-addresses the deployment archive (pkg/versioning/digest.go's
//     isOCIDigestBacked; the exact digest is also recorded on the version as
//     PackageDigest), so there is no separate immutable copy to fall out of
//     sync: the package at that name can only ever resolve to that digest's
//     content, the same guarantee a version-owned snapshot copy exists to
//     provide for mutable archive types.
func (opts *GetSubCommand) getVersion(ctx context.Context, namespace, fnName, versionName string) error {
	version, err := util.GetOwnedFunctionVersion(ctx, opts.Client(), namespace, fnName, versionName)
	if err != nil {
		return err
	}

	ref := version.Spec.Snapshot.Package.PackageRef
	pkg, err := opts.Client().FissionClientSet.CoreV1().Packages(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting package %s/%s for version %q: %w", ref.Namespace, ref.Name, versionName, err)
	}

	os.Stdout.Write(pkg.Spec.Deployment.Literal)

	return nil
}
