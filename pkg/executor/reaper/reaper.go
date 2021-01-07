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

package reaper

import (
	"strings"
	"time"

	"go.uber.org/zap"
	apiv1 "k8s.io/api/core/v1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils"
)

var (
	deletePropagation = meta_v1.DeletePropagationBackground
	delOpt            = meta_v1.DeleteOptions{PropagationPolicy: &deletePropagation}
)

// CleanupKubeObject deletes given kubernetes object
func CleanupKubeObject(logger *zap.Logger, kubeClient *kubernetes.Clientset, kubeobj *apiv1.ObjectReference) {
	switch strings.ToLower(kubeobj.Kind) {
	case "pod":
		err := kubeClient.CoreV1().Pods(kubeobj.Namespace).Delete(kubeobj.Name, nil)
		if err != nil {
			logger.Error("error cleaning up pod", zap.Error(err), zap.String("pod", kubeobj.Name))
		}

	case "service":
		err := kubeClient.CoreV1().Services(kubeobj.Namespace).Delete(kubeobj.Name, nil)
		if err != nil {
			logger.Error("error cleaning up service", zap.Error(err), zap.String("service", kubeobj.Name))
		}

	case "deployment":
		err := kubeClient.AppsV1().Deployments(kubeobj.Namespace).Delete(kubeobj.Name, &delOpt)
		if err != nil {
			logger.Error("error cleaning up deployment", zap.Error(err), zap.String("deployment", kubeobj.Name))
		}

	case "horizontalpodautoscaler":
		err := kubeClient.AutoscalingV1().HorizontalPodAutoscalers(kubeobj.Namespace).Delete(kubeobj.Name, nil)
		if err != nil {
			logger.Error("error cleaning up horizontalpodautoscaler", zap.Error(err), zap.String("horizontalpodautoscaler", kubeobj.Name))
		}

	default:
		logger.Error("Could not identifying the object type to clean up", zap.String("type", kubeobj.Kind), zap.Any("object", kubeobj))

	}
}

// CleanupDeployments deletes deployment(s) for a given instanceID
func CleanupDeployments(logger *zap.Logger, client *kubernetes.Clientset, instanceID string, listOps meta_v1.ListOptions) error {
	deploymentList, err := client.AppsV1().Deployments(meta_v1.NamespaceAll).List(listOps)
	if err != nil {
		return err
	}
	for _, dep := range deploymentList.Items {
		id, ok := dep.ObjectMeta.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL]
		if !ok {
			// Backward compatibility with older label name
			id, ok = dep.ObjectMeta.Labels[fv1.EXECUTOR_INSTANCEID_LABEL]
		}
		if ok && id != instanceID {
			logger.Info("cleaning up deployment", zap.String("deployment", dep.ObjectMeta.Name))
			err := client.AppsV1().Deployments(dep.ObjectMeta.Namespace).Delete(dep.ObjectMeta.Name, &delOpt)
			if err != nil {
				logger.Error("error cleaning up deployment",
					zap.Error(err),
					zap.String("deployment_name", dep.ObjectMeta.Name),
					zap.String("deployment_namespace", dep.ObjectMeta.Namespace))
			}
			// ignore err
		}
	}
	return nil
}

// CleanupPods deletes pod(s) for a given instanceID
func CleanupPods(logger *zap.Logger, client *kubernetes.Clientset, instanceID string, listOps meta_v1.ListOptions) error {
	podList, err := client.CoreV1().Pods(meta_v1.NamespaceAll).List(listOps)
	if err != nil {
		return err
	}
	for _, pod := range podList.Items {
		id, ok := pod.ObjectMeta.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL]
		if !ok {
			// Backward compatibility with older label name
			id, ok = pod.ObjectMeta.Labels[fv1.EXECUTOR_INSTANCEID_LABEL]
		}
		if ok && id != instanceID {
			logger.Info("cleaning up pod", zap.String("pod", pod.ObjectMeta.Name))
			err := client.CoreV1().Pods(pod.ObjectMeta.Namespace).Delete(pod.ObjectMeta.Name, nil)
			if err != nil {
				logger.Error("error cleaning up pod",
					zap.Error(err),
					zap.String("pod_name", pod.ObjectMeta.Name),
					zap.String("pod_namespace", pod.ObjectMeta.Namespace))
			}
			// ignore err
		}
	}
	return nil
}

// CleanupServices deletes service(s) for a given instanceID
func CleanupServices(logger *zap.Logger, client *kubernetes.Clientset, instanceID string, listOps meta_v1.ListOptions) error {
	svcList, err := client.CoreV1().Services(meta_v1.NamespaceAll).List(listOps)
	if err != nil {
		return err
	}
	for _, svc := range svcList.Items {
		id, ok := svc.ObjectMeta.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL]
		if !ok {
			// Backward compatibility with older label name
			id, ok = svc.ObjectMeta.Labels[fv1.EXECUTOR_INSTANCEID_LABEL]
		}
		if ok && id != instanceID {
			logger.Info("cleaning up service", zap.String("service", svc.ObjectMeta.Name))
			err := client.CoreV1().Services(svc.ObjectMeta.Namespace).Delete(svc.ObjectMeta.Name, nil)
			if err != nil {
				logger.Error("error cleaning up service",
					zap.Error(err),
					zap.String("service_name", svc.ObjectMeta.Name),
					zap.String("service_namespace", svc.ObjectMeta.Namespace))
			}
			// ignore err
		}
	}
	return nil
}

// CleanupHpa deletes horizontal pod autoscaler(s) for a given instanceID
func CleanupHpa(logger *zap.Logger, client *kubernetes.Clientset, instanceID string, listOps meta_v1.ListOptions) error {
	hpaList, err := client.AutoscalingV1().HorizontalPodAutoscalers(meta_v1.NamespaceAll).List(listOps)
	if err != nil {
		return err
	}

	for _, hpa := range hpaList.Items {
		id, ok := hpa.ObjectMeta.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL]
		if !ok {
			// Backward compatibility with older label name
			id, ok = hpa.ObjectMeta.Labels[fv1.EXECUTOR_INSTANCEID_LABEL]
		}
		if ok && id != instanceID {
			logger.Info("cleaning up HPA", zap.String("hpa", hpa.ObjectMeta.Name))
			err := client.AutoscalingV1().HorizontalPodAutoscalers(hpa.ObjectMeta.Namespace).Delete(hpa.ObjectMeta.Name, nil)
			if err != nil {
				logger.Error("error cleaning up HPA",
					zap.Error(err),
					zap.String("hpa_name", hpa.ObjectMeta.Name),
					zap.String("hpa_namespace", hpa.ObjectMeta.Namespace))
			}
			// ignore err
		}
	}
	return nil
}

// CleanupRoleBindings periodically lists rolebindings across all namespaces and removes Service Accounts from them or
// deletes the rolebindings completely if there are no Service Accounts in a rolebinding object.
func CleanupRoleBindings(logger *zap.Logger, client *kubernetes.Clientset, fissionClient *crd.FissionClient, functionNs, envBuilderNs string, cleanupRoleBindingInterval time.Duration) {
	for {
		// some sleep before the next reaper iteration
		time.Sleep(cleanupRoleBindingInterval)

		logger.Debug("starting cleanupRoleBindings cycle")
		// get all rolebindings ( just to be efficient, one call to kubernetes )
		rbList, err := client.RbacV1beta1().RoleBindings(meta_v1.NamespaceAll).List(meta_v1.ListOptions{})
		if err != nil {
			// something wrong, but next iteration hopefully succeeds
			logger.Error("error listing role bindings in all namespaces", zap.Error(err))
			continue
		}

		// go through each role-binding object and do the cleanup necessary
		for _, roleBinding := range rbList.Items {
			// ignore role-bindings in kube-system namespace
			if roleBinding.Namespace == "kube-system" {
				continue
			}

			// ignore role-bindings not created by fission
			if roleBinding.Name != fv1.PackageGetterRB && roleBinding.Name != fv1.SecretConfigMapGetterRB {
				continue
			}

			// in order to find out if there are any functions that need this role-binding in role-binding namespace,
			// we can list the functions once per role-binding.
			funcList, err := fissionClient.CoreV1().Functions(roleBinding.Namespace).List(meta_v1.ListOptions{})
			if err != nil {
				logger.Error("error fetching function list in namespace", zap.Error(err), zap.String("namespace", roleBinding.Namespace))
				continue
			}

			// final map of service accounts that can be removed from this roleBinding object
			// using a map here instead of a list so the code in RemoveSAFromRoleBindingWithRetries is efficient.
			saToRemove := make(map[string]bool)

			// the following flags are needed to decide if any of the service accounts can be removed from role-bindings depending on the functions that need them.
			// ndmFunc denotes if there's at least one function that has executor type New deploy Manager
			// funcEnvReference denotes if there's at least one function that has reference to an environment in the SA Namespace for the SA in question
			var ndmFunc, funcEnvReference bool

			// iterate through each subject in the role-binding and check if there are any references to them
			for _, subj := range roleBinding.Subjects {
				ndmFunc = false
				funcEnvReference = false

				// this is the reverse of what we're doing in setting up of role-bindings. if objects are created in default ns,
				// the SA namespace will have the value of "fission-function"/"fission-builder" depending on the SA.
				// so now we need to look for the objects in default namespace.
				saNs := subj.Namespace
				isInReservedNS := false
				if subj.Namespace == functionNs ||
					subj.Namespace == envBuilderNs {
					saNs = meta_v1.NamespaceDefault
					isInReservedNS = true
				}

				// go through each function and find out if there's either at least one function with env reference in the same namespace as the Service Account in this iteration
				// or at least one function using ndm executor in the role-binding namespace and set the corresponding flags
				for _, fn := range funcList.Items {
					if fn.Spec.Environment.Namespace == saNs ||
						//  For the case that the environment is created in the reserved namespace.
						(isInReservedNS && (fn.Spec.Environment.Namespace == functionNs || fn.Spec.Environment.Namespace == envBuilderNs)) {
						funcEnvReference = true
						break
					}

					if fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType == fv1.ExecutorTypeNewdeploy {
						ndmFunc = true
						break
					}
				}

				// if its a package-getter-rb, we have 2 kinds of SAs and each of them is handled differently
				// else if its a secret-configmap-rb, we have only one SA which is fission-fetcher
				if roleBinding.Name == fv1.PackageGetterRB {
					// check if there is an env obj in saNs
					envList, err := fissionClient.CoreV1().Environments(saNs).List(meta_v1.ListOptions{})
					if err != nil {
						logger.Error("error fetching environment list in service account namespace", zap.Error(err), zap.String("namespace", saNs))
						continue
					}

					// if the SA in this iteration is fission-builder, then we need to only check
					// if either there's at least one env object in the SA's namespace, or,
					// if there's at least one function in the role-binding namespace with env reference
					// to the SA's namespace.
					// if neither, then we can remove this SA from this role-binding
					if subj.Name == fv1.FissionBuilderSA {
						if len(envList.Items) == 0 && !funcEnvReference {
							saToRemove[utils.MakeSAMapKey(subj.Name, subj.Namespace)] = true
						}
					}

					// if the SA in this iteration is fission-fetcher, then in addition to above checks,
					// we also need to check if there's at least one function with executor type New deploy
					// in the rolebinding's namespace.
					// if none of them are true, then remove this SA from this role-binding
					if subj.Name == fv1.FissionFetcherSA {
						if len(envList.Items) == 0 && !ndmFunc && !funcEnvReference {
							// remove SA from rolebinding
							saToRemove[utils.MakeSAMapKey(subj.Name, subj.Namespace)] = true
						}
					}
				} else if roleBinding.Name == fv1.SecretConfigMapGetterRB {
					// if there's not even one function in the role-binding's namespace and there's not even
					// one function with env reference to the SA's namespace, then remove that SA
					// from this role-binding
					if !ndmFunc && !funcEnvReference {
						saToRemove[utils.MakeSAMapKey(subj.Name, subj.Namespace)] = true
					}
				}
			}

			// finally, make a call to RemoveSAFromRoleBindingWithRetries for all the service accounts that need to be removed
			// for the role-binding in this iteration
			if len(saToRemove) != 0 {
				logger.Debug("removing service accounts from role binding",
					zap.Any("service_accounts", saToRemove),
					zap.String("role_binding_name", roleBinding.Name),
					zap.String("role_binding_namespace", roleBinding.Namespace))

				// call this once in the end for each role-binding
				err = utils.RemoveSAFromRoleBindingWithRetries(logger, client, roleBinding.Name, roleBinding.Namespace, saToRemove)
				if err != nil {
					// if there's an error, we just log it and proceed with the next role-binding, hoping that this role-binding
					// will be processed in next iteration.
					logger.Debug("error removing service account from role binding",
						zap.Error(err),
						zap.Any("service_accounts", saToRemove),
						zap.String("role_binding_name", roleBinding.Name),
						zap.String("role_binding_namespace", roleBinding.Namespace))
				}
			}
		}
	}
}
