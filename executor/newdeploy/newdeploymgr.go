/*
Copyright 2016 The Fission Authors.

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

package newdeploy

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/fission/fission"
	"github.com/fission/fission/crd"
	"github.com/fission/fission/executor/fscache"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/api"
	apiv1 "k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/rest"
	k8sCache "k8s.io/client-go/tools/cache"
)

type (
	requestType int

	NewDeploy struct {
		kubernetesClient *kubernetes.Clientset
		fissionClient    *crd.FissionClient
		crdClient        *rest.RESTClient
		instanceID       string

		fetcherImg             string
		fetcherImagePullPolicy apiv1.PullPolicy
		namespace              string
		sharedMountPath        string

		fsCache        *fscache.FunctionServiceCache // cache funcSvc's by function, address and podname
		requestChannel chan *fnRequest

		functions      []crd.Function
		funcStore      k8sCache.Store
		funcController k8sCache.Controller
	}

	fnRequest struct {
		reqType         requestType
		fn              *crd.Function
		responseChannel chan *fnResponse
	}

	fnResponse struct {
		error
		fSvc *fscache.FuncSvc
	}
)

const (
	FnCreate requestType = iota
	FnDelete
	FnUpdate
)

func MakeNewDeploy(
	fissionClient *crd.FissionClient,
	kubernetesClient *kubernetes.Clientset,
	crdClient *rest.RESTClient,
	namespace string,
	fsCache *fscache.FunctionServiceCache,
	instanceID string,
) *NewDeploy {

	log.Printf("Creating NewDeploy Backend")

	fetcherImg := os.Getenv("FETCHER_IMAGE")
	if len(fetcherImg) == 0 {
		fetcherImg = "fission/fetcher"
	}
	fetcherImagePullPolicy := os.Getenv("FETCHER_IMAGE_PULL_POLICY")
	if len(fetcherImagePullPolicy) == 0 {
		fetcherImagePullPolicy = "IfNotPresent"
	}

	nd := &NewDeploy{
		fissionClient:    fissionClient,
		kubernetesClient: kubernetesClient,
		crdClient:        crdClient,
		instanceID:       instanceID,

		namespace: namespace,
		fsCache:   fsCache,

		fetcherImg:             fetcherImg,
		fetcherImagePullPolicy: apiv1.PullIfNotPresent,
		sharedMountPath:        "/userfunc",

		requestChannel: make(chan *fnRequest),
	}

	if nd.crdClient != nil {
		fnStore, fnController := nd.initFuncController()
		nd.funcStore = fnStore
		nd.funcController = fnController
	}
	go nd.service()
	return nd
}

func (deploy NewDeploy) Run(ctx context.Context) {
	go deploy.funcController.Run(ctx.Done())
}

func (deploy NewDeploy) initFuncController() (k8sCache.Store, k8sCache.Controller) {
	resyncPeriod := 5 * time.Second
	listWatch := k8sCache.NewListWatchFromClient(deploy.crdClient, "functions", metav1.NamespaceDefault, fields.Everything())
	store, controller := k8sCache.NewInformer(listWatch, &crd.Function{}, resyncPeriod, k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			fn := obj.(*crd.Function)
			if fn.Spec.InvokeStrategy.ExecutionStrategy.Backend == fission.BackendTypeNewdeploy {
				// Eager creation of function if it is RealTimeApp
				if fn.Spec.InvokeStrategy.ExecutionStrategy.EagerCreation == true {
					log.Printf("Eagerly creating newDeploy objects for function")
					c := make(chan *fnResponse)
					deploy.requestChannel <- &fnRequest{
						fn:              fn,
						reqType:         FnCreate,
						responseChannel: c,
					}
					resp := <-c
					if resp.error != nil {
						log.Printf("Error eager creating function: %v", resp.error)
					}
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			fn := obj.(*crd.Function)
			if fn.Spec.InvokeStrategy.ExecutionStrategy.Backend == fission.BackendTypeNewdeploy {
				c := make(chan *fnResponse)
				deploy.requestChannel <- &fnRequest{
					fn:              fn,
					reqType:         FnDelete,
					responseChannel: c,
				}
				resp := <-c
				if resp.error != nil {
					log.Printf("Error deleing the function: %v", resp.error)
				}
			}
		},
		UpdateFunc: func(newObj interface{}, oldObj interface{}) {
			//TBD
		},
	})
	return store, controller
}

func (deploy NewDeploy) service() {
	for {
		req := <-deploy.requestChannel
		switch req.reqType {
		case FnCreate:
			fsvc, err := deploy.fsCache.GetByFunction(&req.fn.Metadata)
			if err == nil {
				req.responseChannel <- &fnResponse{
					error: err,
					fSvc:  fsvc,
				}
				continue
			}

			env, err := deploy.fissionClient.
				Environments(req.fn.Spec.Environment.Namespace).
				Get(req.fn.Spec.Environment.Name)
			if err != nil {
				log.Printf("Error retrieving the environment: %v", err)
				req.responseChannel <- &fnResponse{
					error: err,
					fSvc:  nil,
				}
				continue
			}

			objName := fmt.Sprintf("%v-%v",
				env.Metadata.Name,
				req.fn.Metadata.Name)

			deployLables := map[string]string{
				"environmentName":                 env.Metadata.Name,
				"environmentUid":                  string(env.Metadata.UID),
				"functioName":                     req.fn.Metadata.Name,
				fission.EXECUTOR_INSTANCEID_LABEL: deploy.instanceID,
			}

			depl, err := deploy.createOrGetDeployment(req.fn, env, objName, deployLables)
			if err != nil {
				log.Printf("Error creating the deployment %v: %v", objName, err)
				req.responseChannel <- &fnResponse{
					error: err,
					fSvc:  nil,
				}
				continue
			}

			svc, err := deploy.createOrGetSvc(deployLables, objName)
			if err != nil {
				log.Printf("Error creating the service %v: %v", objName, err)
				req.responseChannel <- &fnResponse{
					error: err,
					fSvc:  nil,
				}
				continue
			}
			svcAddress := svc.Spec.ClusterIP

			_, err = deploy.createHpa(objName, req.fn.Spec.InvokeStrategy.ExecutionStrategy, *depl)
			if err != nil {
				log.Printf("Error creating the HPA %v: %v", objName, err)
				req.responseChannel <- &fnResponse{
					error: err,
					fSvc:  nil,
				}
				continue
			}

			kubeObjRef := api.ObjectReference{
				Kind:            depl.TypeMeta.Kind,
				Name:            depl.ObjectMeta.Name,
				APIVersion:      depl.TypeMeta.APIVersion,
				Namespace:       depl.ObjectMeta.Namespace,
				ResourceVersion: depl.ObjectMeta.ResourceVersion,
				UID:             depl.ObjectMeta.UID,
			}

			fsvc = &fscache.FuncSvc{
				Function:         &req.fn.Metadata,
				Environment:      env,
				Address:          svcAddress,
				KubernetesObject: kubeObjRef,
				Ctime:            time.Now(),
				Atime:            time.Now(),
			}

			_, err = deploy.fsCache.Add(*fsvc)
			if err != nil {
				log.Printf("Error adding the function to cache: %v", err)
				req.responseChannel <- &fnResponse{
					error: err,
					fSvc:  nil,
				}
				continue
			}

			req.responseChannel <- &fnResponse{
				error: nil,
				fSvc:  fsvc,
			}

		case FnUpdate:
			// TBD
		case FnDelete:
			objName := fmt.Sprintf("%v-%v",
				req.fn.Spec.Environment.Name,
				req.fn.Metadata.Name)
			log.Printf("Deleting objects with name: %v", objName)

			err := deploy.deleteDeployment(deploy.namespace, objName)
			if err != nil {
				log.Printf("Error deleting the deployment: %v", objName)
				req.responseChannel <- &fnResponse{
					error: err,
					fSvc:  nil,
				}
				continue
			}

			err = deploy.deleteSvc(deploy.namespace, objName)
			if err != nil {
				log.Printf("Error deleting the service: %v", objName)
				req.responseChannel <- &fnResponse{
					error: err,
					fSvc:  nil,
				}
				continue
			}

			err = deploy.deleteHpa(deploy.namespace, objName)
			if err != nil {
				log.Printf("Error deleting the HPA: %v", objName)
				req.responseChannel <- &fnResponse{
					error: err,
					fSvc:  nil,
				}
				continue
			}

			fsvc, err := deploy.fsCache.GetByFunction(&req.fn.Metadata)
			if err != nil {
				log.Printf("fsvc not fonud in cache: %v", req.fn.Metadata)
				req.responseChannel <- &fnResponse{
					error: err,
					fSvc:  nil,
				}
				continue
			}
			_, err = deploy.fsCache.DeleteByKubeObject(fsvc.KubernetesObject, time.Second*0)
			if err != nil {
				log.Printf("Error deleting the Kubernetes Object from cache: %v", fsvc.KubernetesObject)
				req.responseChannel <- &fnResponse{
					error: err,
					fSvc:  nil,
				}
				continue
			}

			req.responseChannel <- &fnResponse{
				error: nil,
				fSvc:  nil,
			}

		}
	}
}

func (deploy NewDeploy) GetFuncSvc(metadata *metav1.ObjectMeta) (*fscache.FuncSvc, error) {
	c := make(chan *fnResponse)
	fn, err := deploy.fissionClient.
		Functions(metadata.Namespace).
		Get(metadata.Name)
	if err != nil {
		return nil, err
	}
	deploy.requestChannel <- &fnRequest{
		fn:              fn,
		reqType:         FnCreate,
		responseChannel: c,
	}
	resp := <-c
	if resp.error != nil {
		return nil, resp.error
	}
	return resp.fSvc, nil
}
