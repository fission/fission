// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package _package

import (
	"context"
	"fmt"
	"time"

	"errors"

	apiv1 "k8s.io/api/core/v1"
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
	_, opts.pkgNamespace, err = opts.GetResourceNamespace(input)
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

	if input.Bool(flagkey.PkgWatch) {
		// UpdatePackage sets BuildStatusPending synchronously (via
		// UpdateStatus, which also stamps LastUpdateTimestamp) whenever the
		// update triggers a rebuild. A fresh read that is still non-terminal
		// obviously needs watching; a fresh read that is ALREADY terminal is
		// only "nothing to watch" when the status timestamp did not move —
		// an advanced timestamp means this update did trigger a build and
		// buildermgr merely finished (or failed) it before our read, so the
		// watch must still run to report the outcome and set the exit code.
		fresh, err := opts.Client().FissionClientSet.CoreV1().Packages(opts.pkgNamespace).Get(input.Context(), newPkgMeta.Name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("error getting package after update: %w", err)
		}
		buildTriggered := IsBuildInFlight(fresh.Status.BuildStatus) ||
			!fresh.Status.LastUpdateTimestamp.Equal(&pkg.Status.LastUpdateTimestamp)
		if !buildTriggered {
			fmt.Fprintf(input.Stdout(), "Update of package '%v' did not trigger a build; nothing to watch\n", fresh.Name)
			return nil
		}
		return WatchPackageBuild(input, opts.Client(), fresh.Namespace, fresh.Name)
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
	deploySecret := input.String(flagkey.PkgDeploySecret)
	srcSecret := input.String(flagkey.PkgSrcSecret)
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

	if input.IsSet(flagkey.PkgSrcOCI) {
		if input.IsSet(flagkey.PkgCode) || input.IsSet(flagkey.PkgSrcArchive) || input.IsSet(flagkey.PkgOCI) {
			return nil, fmt.Errorf("--%v cannot be combined with --%v, --%v, or --%v", flagkey.PkgSrcOCI, flagkey.PkgCode, flagkey.PkgSrcArchive, flagkey.PkgOCI)
		}
		// Replace the source archive wholesale (RFC-0031 phase 2): an OCI
		// source carries no literal, URL, checksum, or SecretRef.
		image, digest := splitImageDigest(input.String(flagkey.PkgSrcOCI))
		pkg.Spec.Source = fv1.Archive{
			Type: fv1.ArchiveTypeOCI,
			OCI:  &fv1.OCIArchive{Image: image, Digest: digest},
		}
		needToRebuild = true
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
		if srcSecret != "" {
			if err := secretFlagRequiresURL(flagkey.PkgSrcSecret, flagkey.PkgSrcArchive, srcArchiveFiles); err != nil {
				return nil, err
			}
		}
		srcArchive, err := CreateArchive(client, input, srcArchiveFiles, noZip, insecure || srcSecret != "", srcChecksum, "", "", pkg.Namespace)
		if err != nil {
			return nil, fmt.Errorf("error creating source archive: %w", err)
		}
		if srcSecret != "" {
			attachArchiveSecret(srcArchive, srcSecret, srcChecksum)
		}
		pkg.Spec.Source = *srcArchive
		needToRebuild = true
		needToUpdate = true
	} else if input.IsSet(flagkey.PkgSrcSecret) {
		// Attach, rotate, or (with an empty value) detach the credential on
		// the existing url source archive without re-specifying it. A
		// checksum passed in the same command is applied too (the else-if
		// with PkgSrcChecksum below would otherwise be shadowed).
		if pkg.Spec.Source.URL == "" {
			return nil, fmt.Errorf("--%v: package has no url source archive to attach credentials to", flagkey.PkgSrcSecret)
		}
		if srcSecret == "" {
			pkg.Spec.Source.SecretRef = nil
		} else {
			pkg.Spec.Source.SecretRef = &apiv1.LocalObjectReference{Name: srcSecret}
		}
		if input.IsSet(flagkey.PkgSrcChecksum) {
			pkg.Spec.Source.Checksum = fv1.Checksum{Type: fv1.ChecksumTypeSHA256, Sum: srcChecksum}
		}
		// Rotating the source credential is the documented way to fix a build
		// that failed on a 401: re-queue it, since SecretRef is (by design)
		// excluded from the content hash so nothing else would.
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
		if deploySecret != "" {
			if err := secretFlagRequiresURL(flagkey.PkgDeploySecret, flagkey.PkgDeployArchive, deployArchiveFiles); err != nil {
				return nil, err
			}
		}
		deployArchive, err := CreateArchive(client, input, deployArchiveFiles, noZip, insecure || deploySecret != "", deployChecksum, "", "", pkg.Namespace)
		if err != nil {
			return nil, fmt.Errorf("error creating deploy archive: %w", err)
		}
		if deploySecret != "" {
			attachArchiveSecret(deployArchive, deploySecret, deployChecksum)
		}
		pkg.Spec.Deployment = *deployArchive
		// Users may update the env, envNS and deploy archive at the same time,
		// but without the source archive. In this case, we should set needToBuild to false
		needToRebuild = false
		needToUpdate = true
	} else if input.IsSet(flagkey.PkgDeploySecret) {
		// Attach, rotate, or (with an empty value) detach the credential on
		// the existing url deploy archive without re-specifying it. A
		// checksum passed in the same command is applied too (the else-if
		// with PkgDeployChecksum below would otherwise be shadowed).
		if pkg.Spec.Deployment.URL == "" {
			return nil, fmt.Errorf("--%v: package has no url deploy archive to attach credentials to", flagkey.PkgDeploySecret)
		}
		if deploySecret == "" {
			pkg.Spec.Deployment.SecretRef = nil
		} else {
			pkg.Spec.Deployment.SecretRef = &apiv1.LocalObjectReference{Name: deploySecret}
		}
		if input.IsSet(flagkey.PkgDeployChecksum) {
			pkg.Spec.Deployment.Checksum = fv1.Checksum{Type: fv1.ChecksumTypeSHA256, Sum: deployChecksum}
		}
		needToUpdate = true
	} else if input.IsSet(flagkey.PkgDeployChecksum) {
		pkg.Spec.Deployment.Checksum = fv1.Checksum{
			Type: fv1.ChecksumTypeSHA256,
			Sum:  deployChecksum,
		}
		needToUpdate = true
	}

	if !needToUpdate {
		// NOTE: this returns the CALLER'S OWN ObjectMeta, unchanged — nothing
		// was written, so there is no new ResourceVersion to report. Callers
		// must not treat "returned meta == passed-in meta" as "nothing to do":
		// `fn update --pkg <other-package>` lands here (it re-points a function
		// at a package it does not modify) and still has to re-stamp the
		// function's PackageRef, or the function keeps serving the old package.
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

		if existing := fr.PackageInSpecs(pkg, true, true); existing != nil {
			fmt.Printf("Re-using previously created package %s\n", existing.Name)
			return &existing.ObjectMeta, nil
		}

		err = spec.SpecSave(*pkg, specFile, true)
		if err != nil {
			return nil, fmt.Errorf("error saving package spec: %w", err)
		}
		return &pkg.ObjectMeta, nil
	}

	packages := client.FissionClientSet.CoreV1().Packages(pkg.Namespace)

	newPkg, err := util.UpdateOnConflict(input.Context(), packages, pkg.Name,
		func(cur *fv1.Package) { cur.Spec = pkg.Spec })
	if err != nil {
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
		rv := pkgMeta.ResourceVersion
		_, err := util.UpdateOnConflict(ctx, client.FissionClientSet.CoreV1().Functions(fn.Namespace), fn.Name,
			func(cur *fv1.Function) { cur.Spec.Package.PackageRef.ResourceVersion = rv })
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
