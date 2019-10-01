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
	"github.com/pkg/errors"
	"go.uber.org/zap"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/builder"
	builderClient "github.com/fission/fission/pkg/builder/client"
	"github.com/fission/fission/pkg/crd"
	ferror "github.com/fission/fission/pkg/error"
	fetcherClient "github.com/fission/fission/pkg/fetcher/client"
	"github.com/fission/fission/pkg/types"
)

// buildPackage helps to build source package into deployment package.
// Following is the steps buildPackage function takes to complete the whole process.
// 1. Send fetch request to fetcher to fetch source package.
// 2. Send build request to builder to start a build.
// 3. Send upload request to fetcher to upload deployment package.
// 4. Return upload response and build logs.
// *. Return build logs and error if any one of steps above failed.
func buildPackage(ctx context.Context, logger *zap.Logger, fissionClient *crd.FissionClient, envBuilderNamespace string,
	storageSvcUrl string, pkg *fv1.Package) (uploadResp *types.ArchiveUploadResponse, buildLogs string, err error) {

	env, err := fissionClient.Environments(pkg.Spec.Environment.Namespace).Get(pkg.Spec.Environment.Name)
	if err != nil {
		e := "error getting environment CRD info"
		logger.Error(e, zap.Error(err))
		e = fmt.Sprintf("%s: %v", e, err)
		return nil, e, ferror.MakeError(http.StatusInternalServerError, e)
	}

	svcName := fmt.Sprintf("%v-%v.%v", env.Metadata.Name, env.Metadata.ResourceVersion, envBuilderNamespace)
	srcPkgFilename := fmt.Sprintf("%v-%v", pkg.Metadata.Name, strings.ToLower(uniuri.NewLen(6)))
	fetcherC := fetcherClient.MakeClient(logger, fmt.Sprintf("http://%v:8000", svcName))
	builderC := builderClient.MakeClient(logger, fmt.Sprintf("http://%v:8001", svcName))

	fetchReq := &types.FunctionFetchRequest{
		FetchType:   types.FETCH_SOURCE,
		Package:     pkg.Metadata,
		Filename:    srcPkgFilename,
		KeepArchive: false,
	}

	// send fetch request to fetcher
	err = fetcherC.Fetch(ctx, fetchReq)
	if err != nil {
		e := "error fetching source package"
		logger.Error(e, zap.Error(err))
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

	logger.Info("started building with source package", zap.String("source_package", srcPkgFilename))
	// send build request to builder
	buildResp, err := builderC.Build(pkgBuildReq)
	if err != nil {
		e := fmt.Sprintf("Error building deployment package: %v", err)
		var buildLogs string
		if buildResp != nil {
			buildLogs = buildResp.BuildLogs
		}
		buildLogs += fmt.Sprintf("%v\n", e)
		return nil, buildLogs, ferror.MakeError(http.StatusInternalServerError, e)
	}

	logger.Info("build succeed", zap.String("source_package", srcPkgFilename), zap.String("deployment_package", buildResp.ArtifactFilename))

	archivePackage := !env.Spec.KeepArchive

	uploadReq := &types.ArchiveUploadRequest{
		Filename:       buildResp.ArtifactFilename,
		StorageSvcUrl:  storageSvcUrl,
		ArchivePackage: archivePackage,
	}

	logger.Info("started uploading deployment package", zap.String("deployment_package", buildResp.ArtifactFilename))
	// ask fetcher to upload the deployment package
	uploadResp, err = fetcherC.Upload(ctx, uploadReq)
	if err != nil {
		e := fmt.Sprintf("Error uploading deployment package: %v", err)
		buildResp.BuildLogs += fmt.Sprintf("%v\n", e)
		return nil, buildResp.BuildLogs, ferror.MakeError(http.StatusInternalServerError, e)
	}

	return uploadResp, buildResp.BuildLogs, nil
}

func updatePackage(logger *zap.Logger, fissionClient *crd.FissionClient,
	pkg *fv1.Package, status fv1.BuildStatus, buildLogs string,
	uploadResp *types.ArchiveUploadResponse) (*fv1.Package, error) {

	pkg.Status = fv1.PackageStatus{
		BuildStatus:         status,
		BuildLog:            buildLogs,
		LastUpdateTimestamp: time.Now().UTC(),
	}

	if uploadResp != nil {
		pkg.Spec.Deployment = fv1.Archive{
			Type:     types.ArchiveTypeUrl,
			URL:      uploadResp.ArchiveDownloadUrl,
			Checksum: uploadResp.Checksum,
		}
	}

	// update package spec
	pkg, err := fissionClient.Packages(pkg.Metadata.Namespace).Update(pkg)
	if err != nil {
		e := "error updating package"
		logger.Error(e, zap.Error(err))
		return nil, errors.Wrap(err, e)
	}

	// return resource version for function to update function package ref
	return pkg, nil
}
