package buildermgr

import (
	"encoding/json"
	"errors"
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
	"k8s.io/client-go/1.5/pkg/api/v1"
	"k8s.io/client-go/1.5/pkg/apis/extensions/v1beta1"
	"k8s.io/client-go/1.5/pkg/labels"
	"k8s.io/client-go/1.5/pkg/util/intstr"

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
		Function api.ObjectMeta `json:"function"`
	}

	EnvBuilderRequest struct {
		Environment api.ObjectMeta `json:"environment"`
	}

	BuilderMgr struct {
		fissionClient          *tpr.FissionClient
		kubernetesClient       *kubernetes.Clientset
		storageSvcUrl          string
		namespace              string
		fetcherImage           string
		fetcherImagePullPolicy v1.PullPolicy
	}
)

func MakeBuilderMgr(fissionClient *tpr.FissionClient,
	kubernetesClient *kubernetes.Clientset, storageSvcUrl string) *BuilderMgr {

	fetcherImage := os.Getenv("FETCHER_IMAGE")
	if len(fetcherImage) == 0 {
		fetcherImage = "fission/fetcher"
	}
	fetcherImagePullPolicyS := os.Getenv("FETCHER_IMAGE_PULL_POLICY")
	if len(fetcherImagePullPolicyS) == 0 {
		fetcherImagePullPolicyS = "IfNotPresent"
	}

	var pullPolicy v1.PullPolicy
	switch fetcherImagePullPolicyS {
	case "Always":
		pullPolicy = v1.PullAlways
	case "Never":
		pullPolicy = v1.PullNever
	default:
		pullPolicy = v1.PullIfNotPresent
	}

	return &BuilderMgr{
		fissionClient:          fissionClient,
		kubernetesClient:       kubernetesClient,
		storageSvcUrl:          storageSvcUrl,
		namespace:              EnvBuilderNamespace,
		fetcherImage:           fetcherImage,
		fetcherImagePullPolicy: pullPolicy,
	}
}

func (builderMgr *BuilderMgr) getEnvInfo(builderReq *EnvBuilderRequest) (*tpr.Environment, error) {
	env, err := builderMgr.fissionClient.Environments(
		builderReq.Environment.GetNamespace()).Get(builderReq.Environment.GetName())
	if err != nil {
		return nil, errors.New(fmt.Sprintf("Error getting environment TPR info: %v", err))
	}
	return env, nil
}

func (builderMgr *BuilderMgr) createBuilder(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request", 500)
		return
	}

	builderReq := EnvBuilderRequest{}
	err = json.Unmarshal([]byte(body), &builderReq)
	if err != nil {
		http.Error(w, "invalid request body", 400)
		return
	}

	env, err := builderMgr.getEnvInfo(&builderReq)
	if err != nil {
		log.Println(err.Error())
		http.Error(w, err.Error(), 500)
		return
	}

	if len(env.Spec.Builder.Image) == 0 {
		e := "empty builder image"
		log.Println(e)
		http.Error(w, e, 400)
		return
	}

	err = builderMgr.createBuilderService(env)
	if err != nil {
		e := fmt.Sprintf("Error creating builder service: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}

	err = builderMgr.createBuilderDeployment(env)
	if err != nil {
		e := fmt.Sprintf("Error creating builder deployment: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}

	w.WriteHeader(http.StatusOK)
	return
}

func (builderMgr *BuilderMgr) deleteBuilder(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request", 500)
		return
	}

	builderReq := EnvBuilderRequest{}
	err = json.Unmarshal([]byte(body), &builderReq)
	if err != nil {
		http.Error(w, "invalid request body", 400)
		return
	}

	env, err := builderMgr.getEnvInfo(&builderReq)
	if err != nil {
		log.Println(err.Error())
		http.Error(w, err.Error(), 500)
		return
	}

	// cascading deletion
	// https://kubernetes.io/docs/concepts/workloads/controllers/garbage-collection/
	falseVal := false
	deleteOptions := &api.DeleteOptions{
		OrphanDependents: &falseVal,
	}

	sel := make(map[string]string)
	sel["env-builder"] = env.Metadata.Name

	svcList, err := builderMgr.kubernetesClient.Services(builderMgr.namespace).List(
		api.ListOptions{
			LabelSelector: labels.Set(sel).AsSelector(),
		})

	if err != nil {
		e := fmt.Sprintf("Error getting builder service list: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}

	if len(svcList.Items) > 0 {
		err = builderMgr.kubernetesClient.Services(builderMgr.namespace).Delete(env.Metadata.Name, deleteOptions)
		if err != nil {
			e := fmt.Sprintf("Error deleting builder service: %v", err)
			log.Println(e)
			http.Error(w, e, 500)
			return
		}
	}

	deployList, err := builderMgr.kubernetesClient.Deployments(builderMgr.namespace).List(
		api.ListOptions{
			LabelSelector: labels.Set(sel).AsSelector(),
		})

	if err != nil {
		e := fmt.Sprintf("Error getting builder deployment list: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}

	if len(deployList.Items) > 0 {
		err = builderMgr.kubernetesClient.Deployments(builderMgr.namespace).Delete(env.Metadata.Name, deleteOptions)
		if err != nil {
			e := fmt.Sprintf("Error deleteing builder deployment: %v", err)
			log.Println(e)
			http.Error(w, e, 500)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	return
}

func (builderMgr *BuilderMgr) updateBuilder(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request", 500)
		return
	}

	builderReq := EnvBuilderRequest{}
	err = json.Unmarshal([]byte(body), &builderReq)
	if err != nil {
		http.Error(w, "invalid request body", 400)
		return
	}

	env, err := builderMgr.getEnvInfo(&builderReq)
	if err != nil {
		log.Println(err.Error())
		http.Error(w, err.Error(), 500)
		return
	}

	err = builderMgr.updateBuilderDeployment(env)
	if err != nil {
		e := fmt.Sprintf("Error updating builder deployment: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}

	w.WriteHeader(http.StatusOK)
	return
}

func (builderMgr *BuilderMgr) build(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request", 500)
		return
	}

	buildReq := BuildRequest{}
	err = json.Unmarshal([]byte(body), &buildReq)
	if err != nil {
		http.Error(w, "invalid request body", 400)
		return
	}

	fn, err := builderMgr.fissionClient.Functions(
		buildReq.Function.GetNamespace()).Get(buildReq.Function.GetName())
	if err != nil {
		http.Error(w, "Error getting function TPR info", 500)
		return
	}

	// ignore function with non-empty deployment package
	if len(fn.Spec.Deployment.PackageRef.Name) > 0 {
		http.Error(w, "deployment package is not empty", 400)
		return
	}

	srcPkgFilename := fmt.Sprintf("%v-%v", fn.Metadata.Name, strings.ToLower(uniuri.NewLen(6)))

	svc, err := builderMgr.kubernetesClient.Services(builderMgr.namespace).Get(fn.Spec.EnvironmentName)
	if err != nil {
		http.Error(w, "Error getting service TPR info", 500)
		return
	}
	svcIP := svc.Spec.ClusterIP
	fetcherC := fetcherClient.MakeClient(fmt.Sprintf("http://%v:8000", svcIP))
	builderC := builderClient.MakeClient(fmt.Sprintf("http://%v:8001", svcIP))

	fetchReq := &fetcher.FetchRequest{
		FetchType: fetcher.FETCH_SOURCE,
		Function:  fn.Metadata,
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

	pkgRef, err := builderMgr.createPackageFromUrl(fn.Metadata.Name,
		uploadResp.ArchiveDownloadUrl, uploadResp.Checksum)
	if err != nil {
		e := fmt.Sprintf("Error creating deployment package TPR resource: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}

	// Copy the FunctionName from fn.Spec.Source to fn.Spec.Deployment.
	if len(fn.Spec.Source.FunctionName) != 0 {
		pkgRef.FunctionName = fn.Spec.Source.FunctionName
	}
	fn.Spec.Deployment = *pkgRef

	_, err = builderMgr.fissionClient.Functions(fn.Metadata.Namespace).Update(fn)
	if err != nil {
		e := fmt.Sprintf("Error updating function deployment package spec: %v", err)
		log.Println(e)
		http.Error(w, e, 500)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (builderMgr *BuilderMgr) createBuilderService(env *tpr.Environment) error {
	sel := make(map[string]string)
	sel["env-builder"] = env.Metadata.Name
	service := v1.Service{
		ObjectMeta: v1.ObjectMeta{
			Namespace: builderMgr.namespace,
			Name:      env.Metadata.Name,
			Labels:    sel,
		},
		Spec: v1.ServiceSpec{
			Selector: sel,
			Type:     v1.ServiceTypeClusterIP,
			Ports: []v1.ServicePort{
				{
					Name:     "fetcher-port",
					Protocol: v1.ProtocolTCP,
					Port:     8000,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 8000,
					},
				},
				{
					Name:     "builder-port",
					Protocol: v1.ProtocolTCP,
					Port:     8001,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: 8001,
					},
				},
			},
		},
	}
	_, err := builderMgr.kubernetesClient.Services(builderMgr.namespace).Create(&service)
	if err != nil {
		return err
	}
	return nil
}

func (builderMgr *BuilderMgr) createBuilderDeployment(env *tpr.Environment) error {
	deployment := builderMgr.getDeployment(env)
	_, err := builderMgr.kubernetesClient.Deployments(builderMgr.namespace).Create(deployment)
	if err != nil {
		return err
	}
	return nil
}

func (builderMgr *BuilderMgr) updateBuilderDeployment(env *tpr.Environment) error {
	deployment := builderMgr.getDeployment(env)
	_, err := builderMgr.kubernetesClient.Deployments(builderMgr.namespace).Update(deployment)
	if err != nil {
		return err
	}
	return nil
}

func (builderMgr *BuilderMgr) getDeployment(env *tpr.Environment) *v1beta1.Deployment {

	sharedMountPath := "/package"
	sel := make(map[string]string)
	sel["env-builder"] = env.Metadata.Name
	var replicas int32 = 1
	deployment := v1beta1.Deployment{
		ObjectMeta: v1.ObjectMeta{
			Namespace: builderMgr.namespace,
			Name:      env.Metadata.Name,
			Labels:    sel,
		},
		Spec: v1beta1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &v1beta1.LabelSelector{
				MatchLabels: sel,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					Labels: sel,
				},
				Spec: v1.PodSpec{
					Volumes: []v1.Volume{
						{
							Name: "userfunc",
							VolumeSource: v1.VolumeSource{
								EmptyDir: &v1.EmptyDirVolumeSource{},
							},
						},
					},
					Containers: []v1.Container{
						{
							Name:                   "builder",
							Image:                  env.Spec.Builder.Image,
							ImagePullPolicy:        v1.PullAlways,
							TerminationMessagePath: "/dev/termination-log",
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "userfunc",
									MountPath: sharedMountPath,
								},
							},
							Command: []string{"/builder", sharedMountPath},
						},
						{
							Name:                   "fetcher",
							Image:                  builderMgr.fetcherImage,
							ImagePullPolicy:        builderMgr.fetcherImagePullPolicy,
							TerminationMessagePath: "/dev/termination-log",
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "userfunc",
									MountPath: sharedMountPath,
								},
							},
							Command: []string{"/fetcher", sharedMountPath},
						},
					},
					ServiceAccountName: "fission-builder",
				},
			},
		},
	}
	return &deployment
}

// createPackageFromUrl is a function that helps to create a
// TPR package resource, and then return a function package
// reference for further usage.
func (builderMgr *BuilderMgr) createPackageFromUrl(fnName string,
	fileUrl string, checksum fission.Checksum) (*fission.FunctionPackageRef, error) {

	pkgName := fmt.Sprintf("%v-%v", fnName, strings.ToLower(uniuri.NewLen(6)))
	pkg := &tpr.Package{
		Metadata: api.ObjectMeta{
			Name:      pkgName,
			Namespace: api.NamespaceDefault,
		},
		Spec: fission.PackageSpec{
			URL:      fileUrl,
			Checksum: checksum,
		},
	}
	_, err := builderMgr.fissionClient.Packages(api.NamespaceDefault).Create(pkg)
	if err != nil {
		return nil, err
	}
	return &fission.FunctionPackageRef{
		PackageRef: fission.PackageRef{
			Name:      pkgName,
			Namespace: pkg.Metadata.Namespace,
		},
	}, nil
}

func (builderMgr *BuilderMgr) Serve(port int) {
	r := mux.NewRouter()
	r.HandleFunc("/v1/build", builderMgr.build).Methods("POST")
	r.HandleFunc("/v1/builder", builderMgr.createBuilder).Methods("POST")
	r.HandleFunc("/v1/builder", builderMgr.updateBuilder).Methods("PUT")
	r.HandleFunc("/v1/builder", builderMgr.deleteBuilder).Methods("DELETE")
	address := fmt.Sprintf(":%v", port)
	log.Printf("Start buildermgr at port %v", address)
	log.Fatal(http.ListenAndServe(address, handlers.LoggingHandler(os.Stdout, r)))
}
