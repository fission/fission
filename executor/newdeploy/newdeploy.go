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
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	apiv1 "k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/pkg/apis/extensions/v1beta1"

	"github.com/fission/fission/environments/fetcher"
	"github.com/fission/fission/executor/fcache"
	"github.com/fission/fission/tpr"
)

type (
	NewDeploy struct {
		env                    *tpr.Environment
		kubernetesClient       *kubernetes.Clientset
		fissionClient          *tpr.FissionClient
		fetcherImg             string
		fetcherImagePullPolicy apiv1.PullPolicy
		sharedMountPath        string
		initialReplicas        int32
		namespace              string
	}
)

func MakeNewDeploy(
	env *tpr.Environment,
	fissionClient *tpr.FissionClient,
	kubernetesClient *kubernetes.Clientset,
	initialReplicas int32,
	namespace string,
) (*NewDeploy, error) {

	log.Printf("Creating deployment for environment %v", env.Metadata)

	fetcherImg := os.Getenv("FETCHER_IMAGE")
	if len(fetcherImg) == 0 {
		fetcherImg = "fission/fetcher"
	}
	fetcherImagePullPolicy := os.Getenv("FETCHER_IMAGE_PULL_POLICY")
	if len(fetcherImagePullPolicy) == 0 {
		fetcherImagePullPolicy = "IfNotPresent"
	}

	nd := &NewDeploy{
		env:                    env,
		fissionClient:          fissionClient,
		kubernetesClient:       kubernetesClient,
		initialReplicas:        initialReplicas,
		namespace:              namespace,
		fetcherImg:             fetcherImg,
		fetcherImagePullPolicy: apiv1.PullIfNotPresent,
		sharedMountPath:        "/userfunc",
	}

	return nd, nil
}

func (deploy NewDeploy) GetFuncSvc(metadata *metav1.ObjectMeta) (*fcache.FuncSvc, error) {
	fn, err := deploy.fissionClient.
		Functions(metadata.Namespace).
		Get(metadata.Name)
	if err != nil {
		return nil, err
	}
	depl, err := deploy.createNewDeployment(fn)
	if err != nil {
		return nil, err
	}

	fsvc := &fcache.FuncSvc{
		Function:    metadata,
		Environment: deploy.env,
		Address:     "",
		PodName:     depl.ObjectMeta.Name,
		Ctime:       time.Now(),
		Atime:       time.Now(),
	}

	return fsvc, nil
}

func (deploy NewDeploy) createNewDeployment(fn *tpr.Function) (*v1beta1.Deployment, error) {

	poolDeploymentName := fmt.Sprintf("%v-%v",
		deploy.env.Metadata.Name,
		deploy.env.Metadata.UID)

	deployLables := map[string]string{
		"environmentName": deploy.env.Metadata.Name,
		"environmentUid":  string(deploy.env.Metadata.UID),
		"newDeploy":       "true",
	}

	targetFilename := "user"
	req := &fetcher.FetchRequest{
		FetchType: fetcher.FETCH_DEPLOYMENT,
		Package: metav1.ObjectMeta{
			Namespace: fn.Spec.Package.PackageRef.Namespace,
			Name:      fn.Spec.Package.PackageRef.Name,
		},
		Filename: targetFilename,
	}

	payload, err := json.Marshal(req)

	deployment := &v1beta1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   poolDeploymentName,
			Labels: deployLables,
		},
		Spec: v1beta1.DeploymentSpec{
			Replicas: &deploy.initialReplicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: deployLables,
			},
			Template: apiv1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: deployLables,
				},
				Spec: apiv1.PodSpec{
					Volumes: []apiv1.Volume{
						{
							Name: "userfunc",
							VolumeSource: apiv1.VolumeSource{
								EmptyDir: &apiv1.EmptyDirVolumeSource{},
							},
						},
					},
					Containers: []apiv1.Container{
						{
							Name:                   fn.Metadata.Name,
							Image:                  deploy.env.Spec.Runtime.Image,
							ImagePullPolicy:        apiv1.PullIfNotPresent,
							TerminationMessagePath: "/dev/termination-log",
							VolumeMounts: []apiv1.VolumeMount{
								{
									Name:      "userfunc",
									MountPath: deploy.sharedMountPath,
								},
							},
						},
						{
							Name:                   "fetcher",
							Image:                  deploy.fetcherImg,
							ImagePullPolicy:        deploy.fetcherImagePullPolicy,
							TerminationMessagePath: "/dev/termination-log",
							VolumeMounts: []apiv1.VolumeMount{
								{
									Name:      "userfunc",
									MountPath: deploy.sharedMountPath,
								},
							},
							Command: []string{"/fetcher", "-specialize-on-startup",
								"-fetch-request", string(payload),
								deploy.sharedMountPath},
						},
					},
					ServiceAccountName: "fission-fetcher",
				},
			},
		},
	}
	depl, err := deploy.kubernetesClient.ExtensionsV1beta1().Deployments(deploy.namespace).Create(deployment)
	if err != nil {
		return nil, err
	}

	fmt.Println("Deployment=", depl.String())
	return depl, err

}
