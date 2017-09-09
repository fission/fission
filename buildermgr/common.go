package buildermgr

import "github.com/fission/fission/tpr"

func buildPackage(fissionClient *tpr.FissionClient, buildReq BuildRequest) (int, error) {
	return 0, nil
	// pkg, err := fissionClient.Package(
	// 	buildReq.Package.Namespace).Get(buildReq.Package.Name)
	// if err != nil {
	// 	return 500, fmt.Errorf("Error getting package TPR info: %v", err)
	// }

	// if pkg.

	// 	// ignore function with non-empty deployment package
	// 	if len(fn.Spec.Deployment.PackageRef.Name) > 0 {

	// 		e := "deployment package is not empty"
	// 		log.Println(e)
	// 		http.Error(w, e, 400)
	// 		return
	// 	}

	// 	env, err := builderMgr.fissionClient.Environments(api.NamespaceDefault).Get(fn.Spec.EnvironmentName)
	// 	if err != nil {
	// 		e := fmt.Sprintf("Error getting environment TPR info: %v", err)
	// 		log.Println(e)
	// 		http.Error(w, e, 500)
	// 		return
	// 	}

	// 	svcName := fmt.Sprintf("%v-%v", env.Metadata.Name, env.Metadata.ResourceVersion)
	// 	srcPkgFilename := fmt.Sprintf("%v-%v", fn.Metadata.Name, strings.ToLower(uniuri.NewLen(6)))
	// 	svc, err := builderMgr.kubernetesClient.Services(builderMgr.namespace).Get(svcName)
	// 	if err != nil {
	// 		e := fmt.Sprintf("Error getting builder service info %v", err)
	// 		log.Println(e)
	// 		http.Error(w, e, 500)
	// 		return
	// 	}
	// 	svcIP := svc.Spec.ClusterIP
	// 	fetcherC := fetcherClient.MakeClient(fmt.Sprintf("http://%v:8000", svcIP))
	// 	builderC := builderClient.MakeClient(fmt.Sprintf("http://%v:8001", svcIP))

	// 	fetchReq := &fetcher.FetchRequest{
	// 		FetchType: fetcher.FETCH_SOURCE,
	// 		Function:  fn.Metadata,
	// 		Filename:  srcPkgFilename,
	// 	}

	// 	err = fetcherC.Fetch(fetchReq)
	// 	if err != nil {
	// 		e := fmt.Sprintf("Error fetching source package: %v", err)
	// 		log.Println(e)
	// 		http.Error(w, e, 500)
	// 		return
	// 	}

	// 	pkgBuildReq := &builder.PackageBuildRequest{
	// 		SrcPkgFilename: srcPkgFilename,
	// 		BuildCommand:   "build",
	// 	}

	// 	resp, err := builderC.Build(pkgBuildReq)
	// 	if err != nil {
	// 		e := fmt.Sprintf("Error building deployment package: %v", err)
	// 		log.Println(e)
	// 		http.Error(w, e, 500)
	// 		return
	// 	}

	// 	uploadReq := &fetcher.UploadRequest{
	// 		DeployPkgFilename: resp.ArtifactFilename,
	// 		StorageSvcUrl:     builderMgr.storageSvcUrl,
	// 	}

	// 	uploadResp, err := fetcherC.Upload(uploadReq)
	// 	if err != nil {
	// 		e := fmt.Sprintf("Error uploading deployment package: %v", err)
	// 		log.Println(e)
	// 		http.Error(w, e, 500)
	// 		return
	// 	}

	// 	pkgRef, err := builderMgr.createPackageFromUrl(fn.Metadata.Name,
	// 		uploadResp.ArchiveDownloadUrl, uploadResp.Checksum)
	// 	if err != nil {
	// 		e := fmt.Sprintf("Error creating deployment package TPR resource: %v", err)
	// 		log.Println(e)
	// 		http.Error(w, e, 500)
	// 		return
	// 	}

	// 	// Copy the FunctionName from fn.Spec.Source to fn.Spec.Deployment.
	// 	if len(fn.Spec.Source.FunctionName) != 0 {
	// 		pkgRef.FunctionName = fn.Spec.Source.FunctionName
	// 	}
	// 	fn.Spec.Deployment = *pkgRef

	// 	_, err = builderMgr.fissionClient.Functions(fn.Metadata.Namespace).Update(fn)
	// 	if err != nil {
	// 		e := fmt.Sprintf("Error updating function deployment package spec: %v", err)
	// 		log.Println(e)
	// 		http.Error(w, e, 500)
	// 		return
	// 	}
	// }
}
