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
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/dchest/uniuri"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"k8s.io/client-go/1.5/kubernetes"
	"k8s.io/client-go/1.5/pkg/api"

	"github.com/fission/fission"
	"github.com/fission/fission/builder"
	builderClient "github.com/fission/fission/builder/client"
	"github.com/fission/fission/environments/fetcher"
	fetcherClient "github.com/fission/fission/environments/fetcher/client"
	"github.com/fission/fission/tpr"
)

const (
	EnvBuilderNamespace = "fission-builder"
)

type (
	BuildRequest struct {
		Package api.ObjectMeta `json:"package"`
	}

	BuilderMgr struct {
		fissionClient    *tpr.FissionClient
		kubernetesClient *kubernetes.Clientset
		storageSvcUrl    string
		namespace        string
	}
)

func MakeBuilderMgr(fissionClient *tpr.FissionClient,
	kubernetesClient *kubernetes.Clientset, storageSvcUrl string) *BuilderMgr {

	envWatcher := makeEnvironmentWatcher(fissionClient, kubernetesClient, EnvBuilderNamespace)
	go envWatcher.sync()

	return &BuilderMgr{
		fissionClient:    fissionClient,
		kubernetesClient: kubernetesClient,
		storageSvcUrl:    storageSvcUrl,
		namespace:        EnvBuilderNamespace,
	}
}

func (builderMgr *BuilderMgr) build(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		e := fmt.Sprintf("Failed to read request: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}

	buildReq := BuildRequest{}
	err = json.Unmarshal([]byte(body), &buildReq)
	if err != nil {
		e := fmt.Sprintf("invalid request body: %v", err)
		log.Println(e)
		http.Error(w, e, 400)
		return
	}

	pkg, err := builderMgr.fissionClient.Packages(
		buildReq.Package.Namespace).Get(buildReq.Package.Name)
	if err != nil {
		e := fmt.Sprintln("Error getting function TPR info: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}

	// ignore function with non-empty deployment package
	if pkg.Spec.Status.BuildStatus != fission.BuildStatusPending {
		e := "package is not in pending state"
		log.Println(e)
		http.Error(w, e, 400)
		return
	}

	env, err := builderMgr.fissionClient.Environments(api.NamespaceDefault).Get(pkg.Spec.Environment.Name)
	if err != nil {
		e := fmt.Sprintf("Error getting environment TPR info: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}

	svcName := fmt.Sprintf("%v-%v", env.Metadata.Name, env.Metadata.ResourceVersion)
	srcPkgFilename := fmt.Sprintf("%v-%v", pkg.Metadata.Name, strings.ToLower(uniuri.NewLen(6)))
	svc, err := builderMgr.kubernetesClient.Services(builderMgr.namespace).Get(svcName)
	if err != nil {
		e := fmt.Sprintf("Error getting builder service info %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}
	svcIP := svc.Spec.ClusterIP
	fetcherC := fetcherClient.MakeClient(fmt.Sprintf("http://%v:8000", svcIP))
	builderC := builderClient.MakeClient(fmt.Sprintf("http://%v:8001", svcIP))

	fetchReq := &fetcher.FetchRequest{
		FetchType: fetcher.FETCH_SOURCE,
		Package:   pkg.Metadata,
		Filename:  srcPkgFilename,
	}

	err = fetcherC.Fetch(fetchReq)
	if err != nil {
		e := fmt.Sprintf("Error fetching source package: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}

	pkgBuildReq := &builder.PackageBuildRequest{
		SrcPkgFilename: srcPkgFilename,
		BuildCommand:   "build",
	}

	resp, err := builderC.Build(pkgBuildReq)
	if err != nil {
		e := fmt.Sprintf("Error building deployment package: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}

	uploadReq := &fetcher.UploadRequest{
		DeployPkgFilename: resp.ArtifactFilename,
		StorageSvcUrl:     builderMgr.storageSvcUrl,
	}

	uploadResp, err := fetcherC.Upload(uploadReq)
	if err != nil {
		e := fmt.Sprintf("Error uploading deployment package: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}

	newPkgRV, err := builderMgr.updatePackageFromUrl(pkg,
		uploadResp.ArchiveDownloadUrl, uploadResp.Checksum)
	if err != nil {
		e := fmt.Sprintf("Error creating deployment package TPR resource: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}

	fnList, err := builderMgr.fissionClient.
		Functions(api.NamespaceDefault).List(api.ListOptions{})
	if err != nil {
		e := fmt.Sprintf("Error getting function list: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}

	// A package may be used by multiple functions. Update
	// functions with old package resource version
	for _, fn := range fnList.Items {
		if fn.Spec.Package.PackageRef.Name == pkg.Metadata.Name &&
			fn.Spec.Package.PackageRef.ResourceVersion != pkg.Metadata.ResourceVersion {
			fn.Spec.Package.PackageRef.ResourceVersion = newPkgRV
			// update TPR
			_, err = builderMgr.fissionClient.Functions(fn.Metadata.Namespace).Update(&fn)
			if err != nil {
				e := fmt.Sprintf("Error updating function package resource version: %v", err)
				log.Printf(e)
			}
		}
	}

	w.WriteHeader(http.StatusOK)
}

// updatePackageFromUrl is a function that helps to update a
// TPR package resource, and then return a function package
// reference for further usage.
func (builderMgr *BuilderMgr) updatePackageFromUrl(pkg *tpr.Package,
	fileUrl string, checksum fission.Checksum) (string, error) {

	pkg.Spec.Deployment = fission.Archive{
		Type:     fission.ArchiveTypeUrl,
		URL:      fileUrl,
		Checksum: checksum,
	}
	// update package spec
	pkg, err := builderMgr.fissionClient.Packages(api.NamespaceDefault).Update(pkg)
	// return resource version for function to update function package ref
	return pkg.Metadata.ResourceVersion, err
}

func (builderMgr *BuilderMgr) Serve(port int) {
	r := mux.NewRouter()
	r.HandleFunc("/v1/build", builderMgr.build).Methods("POST")
	address := fmt.Sprintf(":%v", port)
	log.Printf("Start buildermgr at port %v", address)
	log.Fatal(http.ListenAndServe(address, handlers.LoggingHandler(os.Stdout, r)))
}
