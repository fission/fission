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

package sdk

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission"
	"github.com/fission/fission/controller/client"
	"github.com/fission/fission/crd"
)

func GetFunctionsByPackage(client *client.Client, pkgName, pkgNamespace string) ([]crd.Function, error) {
	fnList, err := client.FunctionList(pkgNamespace)
	if err != nil {
		return nil, err
	}
	fns := []crd.Function{}
	for _, fn := range fnList {
		if fn.Spec.Package.PackageRef.Name == pkgName {
			fns = append(fns, fn)
		}
	}
	return fns, nil
}

// downloadStoragesvcURL downloads and return archive content with given storage service url
func DownloadStoragesvcURL(client *client.Client, fileUrl string) io.ReadCloser {
	u, err := url.ParseRequestURI(fileUrl)
	if err != nil {
		return nil
	}

	// replace in-cluster storage service host with controller server url
	fileDownloadUrl := strings.TrimSuffix(client.Url, "/") + "/proxy/storage/" + u.RequestURI()
	reader, err := DownloadURL(fileDownloadUrl)

	CheckErr(err, fmt.Sprintf("download from storage service url: %v", fileUrl))
	return reader
}

func UpdatePackage(client *client.Client, pkg *crd.Package, envName, envNamespace,
	srcArchiveName, deployArchiveName, buildcmd string, forceRebuild bool) (*metav1.ObjectMeta, error) {

	var srcArchiveMetadata, deployArchiveMetadata *fission.Archive
	needToBuild := false

	if len(envName) > 0 {
		pkg.Spec.Environment.Name = envName
		needToBuild = true
	}

	if len(envNamespace) > 0 {
		pkg.Spec.Environment.Namespace = envNamespace
		needToBuild = true
	}

	if len(buildcmd) > 0 {
		pkg.Spec.BuildCommand = buildcmd
		needToBuild = true
	}

	if len(srcArchiveName) > 0 {
		var err error
		srcArchiveMetadata, err = CreateArchive(client, srcArchiveName, "")
		if err != nil {
			return nil, err
		}
		pkg.Spec.Source = *srcArchiveMetadata
		needToBuild = true
	}

	if len(deployArchiveName) > 0 {
		var err error
		deployArchiveMetadata, err = CreateArchive(client, deployArchiveName, "")
		if err != nil {
			return nil, err
		}
		pkg.Spec.Deployment = *deployArchiveMetadata
		// Users may update the env, envNS and deploy archive at the same time,
		// but without the source archive. In this case, we should set needToBuild to false
		needToBuild = false
	}

	// Set package as pending status when needToBuild is true
	if needToBuild || forceRebuild {
		// change into pending state to trigger package build
		pkg.Status = fission.PackageStatus{
			BuildStatus: fission.BuildStatusPending,
		}
	}

	newPkgMeta, err := client.PackageUpdate(pkg)

	if err != nil {
		return nil, FailedToError(err, "update package")
	}
	return newPkgMeta, nil
}

func DeleteOrphanPkgs(client *client.Client, pkgNamespace string) error {
	pkgList, err := client.PackageList(pkgNamespace)
	if err != nil {
		return err
	}

	// range through all packages and find out the ones not referenced by any function
	for _, pkg := range pkgList {
		fnList, err := GetFunctionsByPackage(client, pkg.Metadata.Name, pkgNamespace)
		CheckErr(err, fmt.Sprintf("get functions sharing package %s", pkg.Metadata.Name))
		if len(fnList) == 0 {
			err = DeletePackage(client, pkg.Metadata.Name, pkgNamespace)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func DeletePackage(client *client.Client, pkgName string, pkgNamespace string) error {
	return client.PackageDelete(&metav1.ObjectMeta{
		Namespace: pkgNamespace,
		Name:      pkgName,
	})
}
