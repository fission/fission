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
	"fmt"
	"log"
	"strings"

	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/pkg/api"

	"github.com/dchest/uniuri"
	"github.com/fission/fission"
	"github.com/fission/fission/builder"
	builderClient "github.com/fission/fission/builder/client"
	"github.com/fission/fission/environments/fetcher"
	fetcherClient "github.com/fission/fission/environments/fetcher/client"
	"github.com/fission/fission/tpr"
)

// buildPackage helps to build source package into deployment package.
// Following is steps buildPackage takes to complete the build process.
// 1. Check package status
// 2. Update package status to running state
// 3. Send fetch request to fetcher to fetch source package.
// 4. Send build request to builder to start a build.
// 5. Send upload request to fetcher to upload deployment package.
// 6. Update package status to succeed state
// 7. Update package resource in package ref of functions that share the same package
// *. Update package status to failed state,if any one of steps above failed
func buildPackage(fissionClient *tpr.FissionClient, kubernetesClient *kubernetes.Clientset,
	builderNamespace string, storageSvcUrl string, buildReq BuildRequest) (buildLogs string, err error) {

	pkg, err := fissionClient.Packages(
		buildReq.Package.Namespace).Get(buildReq.Package.Name)
	if err != nil {
		e := fmt.Sprintf("Error getting function TPR info: %v", err)
		log.Println(e)
		updatePackage(fissionClient, pkg, fission.BuildStatusFailed, e, nil)
		return e, fission.MakeError(500, e)
	}

	// Only do build for pending packages
	if pkg.Status.BuildStatus != fission.BuildStatusPending {
		e := "package is not in pending state"
		log.Println(e)
		return e, fission.MakeError(400, e)
	}

	// update package status to running state, so that
	// we can know what status a package is through cli.
	_, err = updatePackage(fissionClient, pkg, fission.BuildStatusRunning, "", nil)
	if err != nil {
		e := fmt.Sprintf("Error setting package pending state: %v", err)
		log.Println(e)
		updatePackage(fissionClient, pkg, fission.BuildStatusFailed, e, nil)
		return e, fission.MakeError(500, e)
	}

	env, err := fissionClient.Environments(api.NamespaceDefault).Get(pkg.Spec.Environment.Name)
	if err != nil {
		e := fmt.Sprintf("Error getting environment TPR info: %v", err)
		log.Println(e)
		updatePackage(fissionClient, pkg, fission.BuildStatusFailed, e, nil)
		return e, fission.MakeError(500, e)
	}

	svcName := fmt.Sprintf("%v-%v", env.Metadata.Name, env.Metadata.ResourceVersion)
	srcPkgFilename := fmt.Sprintf("%v-%v", pkg.Metadata.Name, strings.ToLower(uniuri.NewLen(6)))
	svc, err := kubernetesClient.Services(builderNamespace).Get(svcName)
	if err != nil {
		e := fmt.Sprintf("Error getting builder service info %v", err)
		log.Println(e)
		updatePackage(fissionClient, pkg, fission.BuildStatusFailed, e, nil)
		return e, fission.MakeError(500, e)
	}
	svcIP := svc.Spec.ClusterIP
	fetcherC := fetcherClient.MakeClient(fmt.Sprintf("http://%v:8000", svcIP))
	builderC := builderClient.MakeClient(fmt.Sprintf("http://%v:8001", svcIP))

	fetchReq := &fetcher.FetchRequest{
		FetchType: fetcher.FETCH_SOURCE,
		Package:   pkg.Metadata,
		Filename:  srcPkgFilename,
	}

	// send fetch request to fetcher
	err = fetcherC.Fetch(fetchReq)
	if err != nil {
		e := fmt.Sprintf("Error fetching source package: %v", err)
		log.Println(e)
		updatePackage(fissionClient, pkg, fission.BuildStatusFailed, e, nil)
		return e, fission.MakeError(500, e)
	}

	pkgBuildReq := &builder.PackageBuildRequest{
		SrcPkgFilename: srcPkgFilename,
		BuildCommand:   pkg.Spec.BuildCommand,
	}

	// send build request to builder
	buildResp, err := builderC.Build(pkgBuildReq)
	if err != nil {
		e := fmt.Sprintf("Error building deployment package: %v", err)
		log.Println(e)
		updatePackage(fissionClient, pkg, fission.BuildStatusFailed, e, nil)
		return e, fission.MakeError(500, e)
	}

	uploadReq := &fetcher.UploadRequest{
		DeployPkgFilename: buildResp.ArtifactFilename,
		StorageSvcUrl:     storageSvcUrl,
	}

	// ask fetcher to upload the deployment package
	uploadResp, err := fetcherC.Upload(uploadReq)
	if err != nil {
		e := fmt.Sprintf("Error uploading deployment package: %v", err)
		log.Println(e)
		updatePackage(fissionClient, pkg, fission.BuildStatusFailed, e, nil)
		return e, fission.MakeError(500, e)
	}

	// update package status and also build logs
	newPkgRV, err := updatePackage(fissionClient, pkg,
		fission.BuildStatusSucceeded, buildResp.BuildLogs, uploadResp)
	if err != nil {
		e := fmt.Sprintf("Error creating deployment package TPR resource: %v", err)
		log.Println(e)
		updatePackage(fissionClient, pkg, fission.BuildStatusFailed, e, nil)
		return e, fission.MakeError(500, e)
	}

	fnList, err := fissionClient.
		Functions(api.NamespaceDefault).List(api.ListOptions{})
	if err != nil {
		e := fmt.Sprintf("Error getting function list: %v", err)
		log.Println(e)
		updatePackage(fissionClient, pkg, fission.BuildStatusFailed, e, nil)
		return e, fission.MakeError(500, e)
	}

	// A package may be used by multiple functions. Update
	// functions with old package resource version
	for _, fn := range fnList.Items {
		if fn.Spec.Package.PackageRef.Name == pkg.Metadata.Name &&
			fn.Spec.Package.PackageRef.ResourceVersion != pkg.Metadata.ResourceVersion {
			fn.Spec.Package.PackageRef.ResourceVersion = newPkgRV
			// update TPR
			_, err = fissionClient.Functions(fn.Metadata.Namespace).Update(&fn)
			if err != nil {
				e := fmt.Sprintf("Error updating function package resource version: %v", err)
				log.Println(e)
				updatePackage(fissionClient, pkg, fission.BuildStatusFailed, e, nil)
				return e, fission.MakeError(500, e)
			}
		}
	}

	return buildResp.BuildLogs, nil
}

func updatePackage(fissionClient *tpr.FissionClient,
	pkg *tpr.Package, status fission.BuildStatus, buildLogs string,
	uploadResp *fetcher.UploadResponse) (string, error) {

	// Kubernetes checks resource version before applying
	// new resource config. The update operation will be
	// rejected if the resource version in metadata is lower
	// than the latest version. Set resource version empty
	// string to skip resource version check.
	pkg.Metadata.ResourceVersion = ""

	pkg.Status = fission.PackageStatus{
		BuildStatus: status,
		BuildLog:    buildLogs,
	}

	if uploadResp != nil {
		pkg.Spec.Deployment = fission.Archive{
			Type:     fission.ArchiveTypeUrl,
			URL:      uploadResp.ArchiveDownloadUrl,
			Checksum: uploadResp.Checksum,
		}
	}

	// update package spec
	pkg, err := fissionClient.Packages(api.NamespaceDefault).Update(pkg)
	if err != nil {
		log.Printf("Error updating package: %v", err)
		return "", err
	}

	// return resource version for function to update function package ref
	return pkg.Metadata.ResourceVersion, nil
}
