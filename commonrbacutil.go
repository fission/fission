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

package fission

import (
	"log"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/kubernetes"
	apiv1 "k8s.io/client-go/pkg/api/v1"
	rbac "k8s.io/client-go/pkg/apis/rbac/v1beta1"
)

func MakeSAObj(sa, ns string) *apiv1.ServiceAccount {
	return &apiv1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      sa,
		},
	}
}

func SetupSA(k8sClient *kubernetes.Clientset, sa, ns string) (*apiv1.ServiceAccount, error) {
	saObj, err := k8sClient.CoreV1Client.ServiceAccounts(ns).Get(sa, metav1.GetOptions{})
	if err == nil {
		return saObj, nil
	}

	if k8serrors.IsNotFound(err) {
		saObj = MakeSAObj(sa, ns)
		saObj, err = k8sClient.CoreV1Client.ServiceAccounts(ns).Create(saObj)
	}

	return saObj, err
}

func makeRoleBindingObj(roleBinding, roleBindingNs, role, roleKind, sa, saNamespace string) *rbac.RoleBinding {
	return &rbac.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleBinding,
			Namespace: roleBindingNs,
		},
		Subjects: []rbac.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      sa,
				Namespace: saNamespace,
			},
		},
		RoleRef: rbac.RoleRef{
			Kind: roleKind,
			Name: role,
		},
	}
}

func isSAInRoleBinding(rbObj *rbac.RoleBinding, sa, ns string) bool {
	for _, subject := range rbObj.Subjects {
		if subject.Name == sa && subject.Namespace == ns {
			return true
		}
	}

	return false
}

type PatchSpec struct {
	Op    string       `json:"op"`
	Path  string       `json:"path"`
	Value rbac.Subject `json:"value"`
}

func AddSaToRoleBindingWithRetries(k8sClient *kubernetes.Clientset, roleBinding, roleBindingNs, sa, saNamespace, role, roleKind string) error {
	patch := PatchSpec{}
	patch.Op = "add"
	patch.Path = "/subjects/-"
	patch.Value = rbac.Subject{
		Kind:      "ServiceAccount",
		Name:      sa,
		Namespace: saNamespace,
	}

	patchJson, err := json.Marshal([]PatchSpec{patch})
	if err != nil {
		log.Printf("Error marshalling patch into json")
		return err
	}

	for {
		_, err = k8sClient.RbacV1beta1().RoleBindings(roleBindingNs).Patch(roleBinding, types.JSONPatchType, patchJson)
		if err == nil {
			log.Printf("Patched rolebinding : %s.%s", roleBinding, roleBindingNs)
			return err
		}

		if k8serrors.IsNotFound(err) {
			// someone may have deleted the object between us checking if the object is present and deciding to patch
			// so just create the object again
			rbObj := makeRoleBindingObj(roleBinding, roleBindingNs, role, roleKind, sa, saNamespace)
			rbObj, err = k8sClient.RbacV1beta1().RoleBindings(roleBindingNs).Create(rbObj)
			if err == nil {
				log.Printf("Created rolebinding : %s.%s", roleBinding, roleBindingNs)
				return err
			}

			if k8serrors.IsAlreadyExists(err) {
				continue
			}

			log.Printf("Error returned by rolebinding create, %v", err)
			return err
		}

		if k8serrors.IsConflict(err) {
			// TODO : Need to test this out, not able to simulate conflicts yet.
			// Initially, my understanding was that patch can never error on conflict because Api server will handle the conflicts for us.
			// but one CI run did show patch errored out on conflict : https://api.travis-ci.org/v3/job/373161490/log.txt, look for :
			// Error returned by rolebinding patch : <some more text> there is a meaningful conflict (firstResourceVersion: "35482724", currentResourceVersion: "35482849")
			// so, m guessing retrying patch should help. will watch out for any such conflicts and fix the issue if any
			continue
		}

		log.Printf("Error returned by rolebinding patch : %v", err)
		return err
	}
}

func RemoveSAFromRoleBindingWithRetries(k8sClient *kubernetes.Clientset, roleBinding, roleBindingNs, sa, ns string) error {
	for {
		rbObj, err := k8sClient.RbacV1beta1().RoleBindings(roleBindingNs).Get(
			roleBinding, metav1.GetOptions{})
		if err != nil {
			// silently ignoring the error. there's no need for us to remove sa anymore.
			log.Printf("rolebinding %s.%s not found, but ignoring the error since we're cleaning up", roleBinding, roleBindingNs)
			return nil
		}

		subjects := rbObj.Subjects
		newSubjects := make([]rbac.Subject, 0)

		// TODO : optimize it.
		for _, item := range subjects {
			if item.Name == sa && item.Namespace == ns && len(subjects) == 1 {
				return DeleteRoleBinding(k8sClient, roleBinding, roleBindingNs)
			}

			if item.Name == sa && item.Namespace == ns {
				continue
			}

			newSubjects = append(newSubjects, rbac.Subject{
				Kind:      "ServiceAccount",
				Name:      item.Name,
				Namespace: item.Namespace,
			})
		}
		rbObj.Subjects = newSubjects

		// cant use patch for deletes, the results become in-deterministic, so using update.
		_, err = k8sClient.RbacV1beta1().RoleBindings(rbObj.Namespace).Update(rbObj)
		switch {
		case err == nil:
			log.Printf("Removed sa : %s.%s from rolebinding : %s.%s", sa, ns, roleBinding, roleBindingNs)
			return nil
		case k8serrors.IsConflict(err):
			log.Printf("Conflict in update, retrying")
			continue
		default:
			log.Printf("Rolebinding Update Errored out : %v", err)
			return err
		}
	}
}

func SetupRoleBinding(k8sClient *kubernetes.Clientset, roleBinding, roleBindingNs, role, roleKind, sa, saNamespace string) error {
	// get the role binding object
	rbObj, err := k8sClient.RbacV1beta1().RoleBindings(roleBindingNs).Get(
		roleBinding, metav1.GetOptions{})

	if err == nil {
		if !isSAInRoleBinding(rbObj, sa, saNamespace) {
			return AddSaToRoleBindingWithRetries(k8sClient, roleBinding, roleBindingNs, sa, saNamespace, role, roleKind)
		}
		log.Printf("SA : %s.%s already present in rolebinding : %s.%s, so nothing to add", sa, saNamespace, roleBinding, roleBindingNs)
		return nil
	}

	// if role binding is missing, create it. also add this sa to the binding.
	if k8serrors.IsNotFound(err) {
		log.Printf("Rolebinding %s does NOT exist in ns %s. Creating it", roleBinding, roleBindingNs)
		rbObj = makeRoleBindingObj(roleBinding, roleBindingNs, role, roleKind, sa, saNamespace)
		rbObj, err = k8sClient.RbacV1beta1().RoleBindings(roleBindingNs).Create(rbObj)
		if k8serrors.IsAlreadyExists(err) {
			err = AddSaToRoleBindingWithRetries(k8sClient, roleBinding, roleBindingNs, sa, saNamespace, role, roleKind)
		}
	}

	return err
}

func DeleteRoleBinding(k8sClient *kubernetes.Clientset, roleBinding, roleBindingNs string) error {
	// if deleteRoleBinding is invoked by 2 fission services at the same time for the same rolebinding,
	// the first call will succeed while the 2nd will fail with isNotFound. but we dont want to error out then.
	err := k8sClient.RbacV1beta1().RoleBindings(roleBindingNs).Delete(roleBinding, &metav1.DeleteOptions{})
	if err == nil || k8serrors.IsNotFound(err) {
		return nil
	}
	return err
}
