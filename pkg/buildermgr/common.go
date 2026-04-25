/*
Copyright 2017 The Fission Authors.

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

package buildermgr

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dchest/uniuri"
	"github.com/go-logr/logr"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/builder"
	builderClient "github.com/fission/fission/pkg/builder/client"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/fetcher"
	fetcherClient "github.com/fission/fission/pkg/fetcher/client"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

// buildOutcome carries the result of a successful build through to the
// updatePackage call site. Exactly one of TarballUpload or OCI is non-nil.
// The two-channel shape lets the BuildKit branch coexist with the legacy
// tarball flow without changing the package status update path.
type buildOutcome struct {
	TarballUpload *fetcher.ArchiveUploadResponse
	OCI           *fv1.OCIArchive
}

// buildPackage drives a build for a Package, dispatching on the
// Environment's Builder.Kind. The default (empty / "tarball") path
// runs the legacy fetch → build → upload pipeline. Builder.Kind ==
// "buildkit" runs the BuildKit pipeline that produces an OCI artifact
// in a configured registry.
func buildPackage(ctx context.Context, logger logr.Logger, fissionClient versioned.Interface, envBuilderNamespace string,
	storageSvcUrl string, pkg *fv1.Package) (outcome *buildOutcome, buildLogs string, err error) {

	env, err := fissionClient.CoreV1().Environments(pkg.Spec.Environment.Namespace).Get(ctx, pkg.Spec.Environment.Name, metav1.GetOptions{})
	if err != nil {
		e := "error getting environment CRD info"
		logger.Error(err, e)
		e = fmt.Sprintf("%s: %v", e, err)
		return nil, e, ferror.MakeError(http.StatusInternalServerError, e)
	}

	switch env.Spec.Builder.Kind {
	case fv1.BuilderKindBuildKit:
		return buildPackageOCI(ctx, logger, env, envBuilderNamespace, pkg)
	default:
		return buildPackageTarball(ctx, logger, env, envBuilderNamespace, storageSvcUrl, pkg)
	}
}

// buildPackageTarball runs the legacy tarball build pipeline:
// fetch source, run build command in builder sidecar, upload deployment
// archive to storagesvc.
func buildPackageTarball(ctx context.Context, logger logr.Logger, env *fv1.Environment, envBuilderNamespace string,
	storageSvcUrl string, pkg *fv1.Package) (outcome *buildOutcome, buildLogs string, err error) {

	svcName := fmt.Sprintf("%s-%s.%s", env.Name, env.ResourceVersion, envBuilderNamespace)
	srcPkgFilename := fmt.Sprintf("%s-%s", pkg.Name, strings.ToLower(uniuri.NewLen(6)))
	fetcherC := fetcherClient.MakeClient(logger, fmt.Sprintf("http://%s:8000", svcName))
	builderC := builderClient.MakeClient(logger, fmt.Sprintf("http://%s:8001", svcName))

	defer func() {
		logger.Info("cleaning src pkg from builder storage", "source_package", srcPkgFilename)
		errC := cleanPackage(ctx, builderC, srcPkgFilename)
		if errC != nil {
			m := "error cleaning src pkg from builder storage"
			logger.Error(errC, m)
		}
	}()

	fetchReq := &fetcher.FunctionFetchRequest{
		FetchType:   fv1.FETCH_SOURCE,
		Package:     pkg.ObjectMeta,
		Filename:    srcPkgFilename,
		KeepArchive: false,
	}

	if err := fetcherC.Fetch(ctx, fetchReq); err != nil {
		e := "error fetching source package"
		logger.Error(err, e)
		e = fmt.Sprintf("%s: %v", e, err)
		return nil, e, ferror.MakeError(http.StatusInternalServerError, e)
	}

	buildCmd := pkg.Spec.BuildCommand
	if len(buildCmd) == 0 {
		buildCmd = env.Spec.Builder.Command
	}

	pkgBuildReq := &builder.PackageBuildRequest{
		SrcPkgFilename: srcPkgFilename,
		BuildCommand:   buildCmd,
	}

	logger.Info("started building with source package", "source_package", srcPkgFilename)
	buildResp, err := builderC.Build(ctx, pkgBuildReq)
	if err != nil {
		e := fmt.Sprintf("Error building deployment package: %v", err)
		var buildLogs string
		if buildResp != nil {
			buildLogs = buildResp.BuildLogs
		}
		buildLogs += fmt.Sprintf("%v\n", e)
		return nil, buildLogs, ferror.MakeError(http.StatusInternalServerError, e)
	}

	logger.Info("build succeed", "source_package", srcPkgFilename, "deployment_package", buildResp.ArtifactFilename)

	archivePackage := !env.Spec.KeepArchive
	uploadReq := &fetcher.ArchiveUploadRequest{
		Filename:       buildResp.ArtifactFilename,
		StorageSvcUrl:  storageSvcUrl,
		ArchivePackage: archivePackage,
	}

	logger.Info("started uploading deployment package", "deployment_package", buildResp.ArtifactFilename)
	uploadResp, err := fetcherC.Upload(ctx, uploadReq)
	if err != nil {
		e := fmt.Sprintf("Error uploading deployment package: %v", err)
		buildResp.BuildLogs += fmt.Sprintf("%v\n", e)
		return nil, buildResp.BuildLogs, ferror.MakeError(http.StatusInternalServerError, e)
	}

	return &buildOutcome{TarballUpload: uploadResp}, buildResp.BuildLogs, nil
}

// buildPackageOCI drives a buildkit-flavored build. The fetcher still
// pulls the source archive into the shared volume — that's the same
// contract for both pipelines — but the build itself is delegated to
// the builder pod's /buildkit endpoint, which is responsible for
// invoking buildctl and pushing the resulting image to the registry.
//
// Result is written into Package.Spec.Deployment.OCI by updatePackage.
// The image tag is deterministic-per-package: <pkg.Name>-<resourceVersion>-<short-uniuri>,
// which gives users a roughly time-ordered set of artifacts in their
// registry while avoiding overwriting on rebuild.
func buildPackageOCI(ctx context.Context, logger logr.Logger, env *fv1.Environment, envBuilderNamespace string,
	pkg *fv1.Package) (outcome *buildOutcome, buildLogs string, err error) {

	registry := env.Spec.Builder.Registry
	if registry == nil || registry.URL == "" {
		e := "buildkit builder requires Environment.Spec.Builder.Registry.URL to be set"
		return nil, e, ferror.MakeError(http.StatusBadRequest, e)
	}

	svcName := fmt.Sprintf("%s-%s.%s", env.Name, env.ResourceVersion, envBuilderNamespace)
	srcPkgFilename := fmt.Sprintf("%s-%s", pkg.Name, strings.ToLower(uniuri.NewLen(6)))
	fetcherC := fetcherClient.MakeClient(logger, fmt.Sprintf("http://%s:8000", svcName))
	builderC := builderClient.MakeClient(logger, fmt.Sprintf("http://%s:8001", svcName))

	defer func() {
		logger.Info("cleaning src pkg from builder storage", "source_package", srcPkgFilename)
		errC := cleanPackage(ctx, builderC, srcPkgFilename)
		if errC != nil {
			logger.Error(errC, "error cleaning src pkg from builder storage")
		}
	}()

	fetchReq := &fetcher.FunctionFetchRequest{
		FetchType:   fv1.FETCH_SOURCE,
		Package:     pkg.ObjectMeta,
		Filename:    srcPkgFilename,
		KeepArchive: false,
	}
	if err := fetcherC.Fetch(ctx, fetchReq); err != nil {
		e := fmt.Sprintf("error fetching source package: %v", err)
		logger.Error(err, "error fetching source package for buildkit build")
		return nil, e, ferror.MakeError(http.StatusInternalServerError, e)
	}

	baseImage := registry.BaseImage
	if baseImage == "" {
		baseImage = env.Spec.Runtime.Image
	}
	imageRef := fmt.Sprintf("%s/%s:%s-%s",
		strings.TrimSuffix(registry.URL, "/"),
		pkg.Name,
		pkg.ResourceVersion,
		strings.ToLower(uniuri.NewLen(6)))

	logger.Info("started buildkit build", "source_package", srcPkgFilename, "image_ref", imageRef)
	ociResp, err := builderC.BuildOCI(ctx, &builder.OCIBuildRequest{
		SrcPkgFilename: srcPkgFilename,
		ImageRef:       imageRef,
		BaseImage:      baseImage,
		RegistryURL:    registry.URL,
	})
	if err != nil {
		var logs string
		if ociResp != nil {
			logs = ociResp.BuildLogs
		}
		logs += fmt.Sprintf("error building OCI image: %v\n", err)
		return nil, logs, ferror.MakeError(http.StatusInternalServerError, fmt.Sprintf("error building OCI image: %v", err))
	}

	oci := &fv1.OCIArchive{
		Image:  ociResp.ImageRef,
		Digest: ociResp.Digest,
	}
	if registry.ImagePullSecret != "" {
		oci.ImagePullSecrets = append(oci.ImagePullSecrets,
			apiv1.LocalObjectReference{Name: registry.ImagePullSecret})
	}

	logger.Info("buildkit build succeeded",
		"source_package", srcPkgFilename,
		"image_ref", oci.Image,
		"digest", oci.Digest)
	return &buildOutcome{OCI: oci}, ociResp.BuildLogs, nil
}

func cleanPackage(ctx context.Context, builderClient builderClient.ClientInterface, srcPkgFileName string) error {
	err := builderClient.Clean(ctx, srcPkgFileName)
	if err != nil {
		return err
	}

	return nil
}

func updatePackage(ctx context.Context, logger logr.Logger, fissionClient versioned.Interface,
	pkg *fv1.Package, status fv1.BuildStatus, buildLogs string,
	outcome *buildOutcome) (*fv1.Package, error) {

	pkg.Status = fv1.PackageStatus{
		BuildStatus:         status,
		BuildLog:            buildLogs,
		LastUpdateTimestamp: metav1.Time{Time: time.Now().UTC()},
	}

	if outcome != nil {
		switch {
		case outcome.OCI != nil:
			pkg.Spec.Deployment = fv1.Archive{
				Type: fv1.ArchiveTypeOCI,
				OCI:  outcome.OCI,
			}
		case outcome.TarballUpload != nil:
			pkg.Spec.Deployment = fv1.Archive{
				Type:     fv1.ArchiveTypeUrl,
				URL:      outcome.TarballUpload.ArchiveDownloadUrl,
				Checksum: outcome.TarballUpload.Checksum,
			}
		}
	}

	// update package spec
	pkg, err := fissionClient.CoreV1().Packages(pkg.ObjectMeta.Namespace).Update(ctx, pkg, metav1.UpdateOptions{})
	if err != nil {
		e := "error updating package"
		logger.Error(err, e)
		return nil, fmt.Errorf("%s: %w", e, err)
	}

	// return resource version for function to update function package ref
	return pkg, nil
}
