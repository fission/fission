// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package _package

import (
	"context"
	"fmt"
	"time"

	"errors"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type UpdateSubCommand struct {
	cmd.CommandActioner
	pkgName      string
	pkgNamespace string
	force        bool
}

func Update(input cli.Input) error {
	return (&UpdateSubCommand{}).do(input)
}

func (opts *UpdateSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *UpdateSubCommand) complete(input cli.Input) (err error) {
	opts.pkgName = input.String(flagkey.PkgName)
	_, opts.pkgNamespace, err = opts.GetResourceNamespace(input, flagkey.NamespacePackage)
	if err != nil {
		return fv1.AggregateValidationErrors("Package", err)
	}
	opts.force = input.Bool(flagkey.PkgForce)
	return nil
}

func (opts *UpdateSubCommand) run(input cli.Input) error {
	pkgName := input.String(flagkey.PkgName)
	pkg, err := opts.Client().FissionClientSet.CoreV1().Packages(opts.pkgNamespace).Get(input.Context(), opts.pkgName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting package: %w", err)
	}

	forceUpdate := input.Bool(flagkey.PkgForce)

	fnList, err := GetFunctionsByPackage(input.Context(), opts.Client(), pkg.Name, pkg.Namespace)
	if err != nil {
		return fmt.Errorf("error getting function list: %w", err)
	}

	if !forceUpdate && len(fnList) > 1 {
		return fmt.Errorf("package is used by multiple functions, use --%v to force update", flagkey.PkgForce)
	}
	specFile := fmt.Sprintf("package-%s.yaml", pkgName)
	newPkgMeta, err := UpdatePackage(input, opts.Client(), specFile, pkg)
	if err != nil {
		return fmt.Errorf("error updating package: %w", err)
	}

	if pkg.ResourceVersion != newPkgMeta.ResourceVersion {
		err = UpdateFunctionPackageResourceVersion(input.Context(), opts.Client(), newPkgMeta, fnList...)
		if err != nil {
			return fmt.Errorf("error updating function package reference resource version: %w", err)
		}
	}

	return nil
}

func UpdatePackage(input cli.Input, client cmd.Client, specFile string, pkg *fv1.Package) (*metav1.ObjectMeta, error) {
	envName := input.String(flagkey.PkgEnvironment)
	srcArchiveFiles := input.StringSlice(flagkey.PkgSrcArchive)
	deployArchiveFiles := input.StringSlice(flagkey.PkgDeployArchive)
	buildcmd := input.String(flagkey.PkgBuildCmd)
	insecure := input.Bool(flagkey.PkgInsecure)
	deployChecksum := input.String(flagkey.PkgDeployChecksum)
	srcChecksum := input.String(flagkey.PkgSrcChecksum)
	code := input.String(flagkey.PkgCode)

	noZip := false
	needToRebuild := false
	needToUpdate := false

	ociImage := input.String(flagkey.PkgOCI)
	if input.IsSet(flagkey.PkgOCI) {
		if input.IsSet(flagkey.PkgCode) || input.IsSet(flagkey.PkgSrcArchive) || input.IsSet(flagkey.PkgDeployArchive) {
			return nil, fmt.Errorf("--%v cannot be combined with --%v, --%v, or --%v", flagkey.PkgOCI, flagkey.PkgCode, flagkey.PkgSrcArchive, flagkey.PkgDeployArchive)
		}
		// Replace the deployment archive wholesale: an OCI archive carries no
		// literal, URL, or checksum (RFC-0001).
		pkg.Spec.Deployment = fv1.Archive{
			Type: fv1.ArchiveTypeOCI,
			OCI:  &fv1.OCIArchive{Image: ociImage},
		}
		needToUpdate = true
	}

	if input.IsSet(flagkey.PkgCode) {
		deployArchiveFiles = append(deployArchiveFiles, code)
		noZip = true
		needToUpdate = true
	}

	if input.IsSet(flagkey.PkgEnvironment) {
		pkg.Spec.Environment.Name = envName
		needToRebuild = true
		needToUpdate = true
	}

	if input.IsSet(flagkey.PkgBuildCmd) {
		pkg.Spec.BuildCommand = buildcmd
		needToRebuild = true
		needToUpdate = true
	}

	if input.IsSet(flagkey.PkgSrcArchive) {
		srcArchive, err := CreateArchive(client, input, srcArchiveFiles, noZip, insecure, srcChecksum, "", "", pkg.Namespace)
		if err != nil {
			return nil, fmt.Errorf("error creating source archive: %w", err)
		}
		pkg.Spec.Source = *srcArchive
		needToRebuild = true
		needToUpdate = true
	} else if input.IsSet(flagkey.PkgSrcChecksum) {
		pkg.Spec.Source.Checksum = fv1.Checksum{
			Type: fv1.ChecksumTypeSHA256,
			Sum:  srcChecksum,
		}
		needToUpdate = true
	}

	if input.IsSet(flagkey.PkgDeployArchive) || input.IsSet(flagkey.PkgCode) {
		deployArchive, err := CreateArchive(client, input, deployArchiveFiles, noZip, insecure, deployChecksum, "", "", pkg.Namespace)
		if err != nil {
			return nil, fmt.Errorf("error creating deploy archive: %w", err)
		}
		pkg.Spec.Deployment = *deployArchive
		// Users may update the env, envNS and deploy archive at the same time,
		// but without the source archive. In this case, we should set needToBuild to false
		needToRebuild = false
		needToUpdate = true
	} else if input.IsSet(flagkey.PkgDeployChecksum) {
		pkg.Spec.Deployment.Checksum = fv1.Checksum{
			Type: fv1.ChecksumTypeSHA256,
			Sum:  deployChecksum,
		}
		needToUpdate = true
	}

	if !needToUpdate {
		return &pkg.ObjectMeta, nil
	}

	// Set package as pending status when needToBuild is true
	if needToRebuild {
		// change into pending state to trigger package build
		pkg.Status = fv1.PackageStatus{
			BuildStatus:         fv1.BuildStatusPending,
			LastUpdateTimestamp: metav1.Time{Time: time.Now().UTC()},
		}
	}

	if input.Bool(flagkey.SpecSave) {
		// if a package with the same spec exists, don't create a new spec file
		fr, err := spec.ReadSpecs(util.GetSpecDir(input), util.GetSpecIgnore(input), false)
		if err != nil {
			return nil, fmt.Errorf("error reading specs: %w", err)
		}

		obj := fr.SpecExists(pkg, true, true)
		if obj != nil {
			pkg := obj.(*fv1.Package)
			fmt.Printf("Re-using previously created package %s\n", pkg.Name)
			return &pkg.ObjectMeta, nil
		}

		err = spec.SpecSave(*pkg, specFile, true)
		if err != nil {
			return nil, fmt.Errorf("error saving package spec: %w", err)
		}
		return &pkg.ObjectMeta, nil
	}

	packages := client.FissionClientSet.CoreV1().Packages(pkg.Namespace)

	// Apply the desired spec, re-getting on conflict: the buildermgr writes a
	// package's initial build status shortly after create, which bumps the
	// ResourceVersion between our Get and this Update.
	desiredSpec := pkg.Spec
	var newPkg *fv1.Package
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh, gerr := packages.Get(input.Context(), pkg.Name, metav1.GetOptions{})
		if gerr != nil {
			return gerr
		}
		fresh.Spec = desiredSpec
		var uerr error
		newPkg, uerr = packages.Update(input.Context(), fresh, metav1.UpdateOptions{})
		return uerr
	}); err != nil {
		return nil, fmt.Errorf("update package: %w", err)
	}

	// A package switched to an OCI deployment archive needs no build, but a
	// stale build status from its previous life (a failed source build, say)
	// would make the fetcher refuse to serve it. Reset it through the
	// /status subresource — the spec Update above cannot touch status.
	if newPkg.Spec.Deployment.OCI != nil {
		switch newPkg.Status.BuildStatus {
		case fv1.BuildStatusFailed, fv1.BuildStatusPending, fv1.BuildStatusRunning:
			if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				fresh, gerr := packages.Get(input.Context(), pkg.Name, metav1.GetOptions{})
				if gerr != nil {
					return gerr
				}
				fresh.Status.BuildStatus = fv1.BuildStatusNone
				fresh.Status.BuildLog = ""
				fresh.Status.LastUpdateTimestamp = metav1.Time{Time: time.Now().UTC()}
				var uerr error
				newPkg, uerr = packages.UpdateStatus(input.Context(), fresh, metav1.UpdateOptions{})
				return uerr
			}); err != nil {
				return nil, fmt.Errorf("reset package build status for oci archive: %w", err)
			}
		}
	}

	// The rebuild trigger (BuildStatusPending) is a status write; with the
	// /status subresource the spec Update above ignores it, so persist it
	// separately through UpdateStatus (also conflict-retried).
	if needToRebuild {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			fresh, gerr := packages.Get(input.Context(), pkg.Name, metav1.GetOptions{})
			if gerr != nil {
				return gerr
			}
			fresh.Status.BuildStatus = fv1.BuildStatusPending
			fresh.Status.LastUpdateTimestamp = metav1.Time{Time: time.Now().UTC()}
			var uerr error
			newPkg, uerr = packages.UpdateStatus(input.Context(), fresh, metav1.UpdateOptions{})
			return uerr
		}); err != nil {
			return nil, fmt.Errorf("update package status: %w", err)
		}
	}

	fmt.Printf("Package '%v' updated\n", newPkg.GetName())

	return &newPkg.ObjectMeta, nil
}

func UpdateFunctionPackageResourceVersion(ctx context.Context, client cmd.Client, pkgMeta *metav1.ObjectMeta, fnList ...fv1.Function) error {
	var errs error

	// update resource version of package reference of functions that shared the same package
	for _, fn := range fnList {
		fn.Spec.Package.PackageRef.ResourceVersion = pkgMeta.ResourceVersion
		_, err := client.FissionClientSet.CoreV1().Functions(fn.Namespace).Update(ctx, &fn, metav1.UpdateOptions{})
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("error updating package resource version of function '%v': %w", fn.Name, err))
		}
	}

	return errs
}

func updatePackageStatus(ctx context.Context, client cmd.Client, pkg *fv1.Package, status fv1.BuildStatus) (*metav1.ObjectMeta, error) {
	switch status {
	case fv1.BuildStatusNone, fv1.BuildStatusPending, fv1.BuildStatusRunning, fv1.BuildStatusSucceeded, fv1.BuildStatusFailed:
		packages := client.FissionClientSet.CoreV1().Packages(pkg.Namespace)
		var out *fv1.Package
		// Re-get on conflict: the buildermgr can update the package status
		// concurrently.
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			fresh, gerr := packages.Get(ctx, pkg.Name, metav1.GetOptions{})
			if gerr != nil {
				return gerr
			}
			fresh.Status.BuildStatus = status
			fresh.Status.LastUpdateTimestamp = metav1.Time{Time: time.Now().UTC()}
			var uerr error
			out, uerr = packages.UpdateStatus(ctx, fresh, metav1.UpdateOptions{})
			return uerr
		}); err != nil {
			return nil, err
		}
		return &out.ObjectMeta, nil
	}
	return nil, errors.New("unknown package status")
}
