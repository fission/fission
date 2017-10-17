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

package tpr

import (
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/apis/extensions/v1beta1"
)

// ensureTPR checks if the given TPR type exists, and creates it if
// needed. (Note that this creates the TPR type; it doesn't create any
// _instances_ of that type.)
func ensureTPR(clientset *kubernetes.Clientset, tpr *v1beta1.ThirdPartyResource) error {
	_, err := clientset.Extensions().ThirdPartyResources().Get(tpr.ObjectMeta.Name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			_, err := clientset.Extensions().ThirdPartyResources().Create(tpr)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func EnsureFissionTPRs(clientset *kubernetes.Clientset) error {
	tprs := []v1beta1.ThirdPartyResource{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "function.fission.io",
			},
			Versions: []v1beta1.APIVersion{
				{Name: "v1"},
			},
			Description: "Functions",
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "environment.fission.io",
			},
			Versions: []v1beta1.APIVersion{
				{Name: "v1"},
			},
			Description: "Environments (function containers)",
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "httptrigger.fission.io",
			},
			Versions: []v1beta1.APIVersion{
				{Name: "v1"},
			},
			Description: "HTTP triggers for functions",
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "kuberneteswatchtrigger.fission.io",
			},
			Versions: []v1beta1.APIVersion{
				{Name: "v1"},
			},
			Description: "Kubernetes watch triggers for functions",
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "timetrigger.fission.io",
			},
			Versions: []v1beta1.APIVersion{
				{Name: "v1"},
			},
			Description: "Time-based triggers for functions",
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "messagequeuetrigger.fission.io",
			},
			Versions: []v1beta1.APIVersion{
				{Name: "v1"},
			},
			Description: "Message queue triggers for functions",
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "package.fission.io",
			},
			Versions: []v1beta1.APIVersion{
				{Name: "v1"},
			},
			Description: "Packages: archives containing source or binaries for one or more functions",
		},
	}
	for _, tpr := range tprs {
		err := ensureTPR(clientset, &tpr)
		if err != nil {
			return err
		}
	}
	return nil
}
