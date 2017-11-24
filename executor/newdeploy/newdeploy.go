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
	"errors"
	"path/filepath"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	apiv1 "k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/pkg/apis/extensions/v1beta1"

	"github.com/fission/fission"
	"github.com/fission/fission/crd"
	"github.com/fission/fission/environments/fetcher"
)

func (deploy NewDeploy) createOrGetDeployment(fn *crd.Function, env *crd.Environment,
	deployName string, deployLables map[string]string) (*v1beta1.Deployment, error) {

	replicas := int32(1)
	targetFilename := "user"

	existingDepl, err := deploy.kubernetesClient.ExtensionsV1beta1().Deployments(deploy.namespace).Get(deployName, metav1.GetOptions{})
	if err == nil && existingDepl.Status.ReadyReplicas >= replicas {
		return existingDepl, err
	}

	fetchReq := &fetcher.FetchRequest{
		FetchType: fetcher.FETCH_DEPLOYMENT,
		Package: metav1.ObjectMeta{
			Namespace: fn.Spec.Package.PackageRef.Namespace,
			Name:      fn.Spec.Package.PackageRef.Name,
		},
		Filename: targetFilename,
	}

	loadReq := fission.FunctionLoadRequest{
		FilePath:         filepath.Join(deploy.sharedMountPath, targetFilename),
		FunctionName:     fn.Spec.Package.FunctionName,
		FunctionMetadata: &fn.Metadata,
	}

	fetchPayload, err := json.Marshal(fetchReq)
	loadPayload, err := json.Marshal(loadReq)

	deployment := &v1beta1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   deployName,
			Labels: deployLables,
		},
		Spec: v1beta1.DeploymentSpec{
			Replicas: &replicas,
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
							Image:                  env.Spec.Runtime.Image,
							ImagePullPolicy:        apiv1.PullIfNotPresent,
							TerminationMessagePath: "/dev/termination-log",
							VolumeMounts: []apiv1.VolumeMount{
								{
									Name:      "userfunc",
									MountPath: deploy.sharedMountPath,
								},
							},
							Resources: env.Spec.Resources,
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
								"-fetch-request", string(fetchPayload),
								"-load-request", string(loadPayload),
								deploy.sharedMountPath},
							Env: []apiv1.EnvVar{
								{
									Name:  envVersion,
									Value: strconv.Itoa(env.Spec.Version),
								},
							},
							ReadinessProbe: &apiv1.Probe{
								Handler: apiv1.Handler{
									Exec: &apiv1.ExecAction{
										Command: []string{"cat", "/tmp/ready"},
									},
								},
								InitialDelaySeconds: 2,
								PeriodSeconds:       1,
							},
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

	for i := 0; i < 20; i++ {
		latestDepl, err := deploy.kubernetesClient.ExtensionsV1beta1().Deployments(deploy.namespace).Get(depl.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if latestDepl.Status.ReadyReplicas >= replicas {
			return latestDepl, err
		}
		time.Sleep(time.Second)
	}
	return nil, errors.New("Failed to create replicas in deployment")

}

func (deploy NewDeploy) deleteDeployment(ns string, name string) error {
	deletePropagation := metav1.DeletePropagationForeground
	err := deploy.kubernetesClient.ExtensionsV1beta1().Deployments(ns).Delete(name, &metav1.DeleteOptions{
		PropagationPolicy: &deletePropagation,
	})
	if err != nil {
		return err
	}
	return nil
}

func (deploy NewDeploy) createOrGetSvc(deployLables map[string]string, svcName string) (*apiv1.Service, error) {

	existingSvc, err := deploy.kubernetesClient.CoreV1().Services(deploy.namespace).Get(svcName, metav1.GetOptions{})
	if err == nil {
		return existingSvc, err
	}
	service := &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: svcName,
		},
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		Spec: apiv1.ServiceSpec{
			Ports: []apiv1.ServicePort{
				{
					Name:       "pod-port",
					Port:       int32(80),
					TargetPort: intstr.FromInt(8888)},
			},
			Selector: deployLables,
			Type:     apiv1.ServiceTypeClusterIP,
		},
	}

	svc, err := deploy.kubernetesClient.CoreV1().Services(deploy.namespace).Create(service)
	if err != nil {
		return nil, err
	}

	return svc, nil
}

func (deploy NewDeploy) deleteSvc(ns string, name string) error {
	err := deploy.kubernetesClient.CoreV1().Services(ns).Delete(name, &metav1.DeleteOptions{})
	if err != nil {
		return err
	}
	return nil
}
