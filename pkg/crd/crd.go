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

package crd

import (
	"time"

	"go.uber.org/zap"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	crdGroupName = "fission.io"
	crdVersion   = "v1"
	crdVersion2  = "v2"
)

// ensureCRD checks if the given CRD type exists, and creates it if
// needed. (Note that this creates the CRD type; it doesn't create any
// _instances_ of that type.)
func ensureCRD(logger *zap.Logger, clientset *apiextensionsclient.Clientset, crd *apiextensionsv1beta1.CustomResourceDefinition) (err error) {
	maxRetries := 5

	for i := 0; i < maxRetries; i++ {
		_, err = clientset.ApiextensionsV1beta1().CustomResourceDefinitions().Create(crd)
		if err == nil {
			return nil
		}

		// return if the resource already exists
		if k8serrors.IsAlreadyExists(err) {
			return nil
		} else {
			// The requests fail to connect to k8s api server before
			// istio-prxoy is ready to serve traffic. Retry again.
			logger.Info("error connecting to kubernetes api service, retrying", zap.Error(err))
			time.Sleep(500 * time.Duration(2*i) * time.Millisecond)
			continue
		}
	}

	return err
}

// EnsureFissionCRDs creates the CRDs
func EnsureFissionCRDs(logger *zap.Logger, clientset *apiextensionsclient.Clientset) error {

	versions := make([]apiextensionsv1beta1.CustomResourceDefinitionVersion, 0)

	version1 := apiextensionsv1beta1.CustomResourceDefinitionVersion{
		Name:    crdVersion,
		Served:  false,
		Storage: false,
	}

	version2 := apiextensionsv1beta1.CustomResourceDefinitionVersion{
		Name:    crdVersion2,
		Served:  true,
		Storage: true,
	}

	versions = append(versions, version1)
	versions = append(versions, version2)

	conversion := apiextensionsv1beta1.CustomResourceConversion{
		Strategy: apiextensionsv1beta1.ConversionStrategyType("None"),
	}
	crds := []apiextensionsv1beta1.CustomResourceDefinition{
		// Functions
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "functions.fission.io",
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group: crdGroupName,
				Scope: apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Kind:     "Function",
					Plural:   "functions",
					Singular: "function",
				},
				PreserveUnknownFields: boolPtr(false),
				Validation:            functionValidation,
				Versions:              versions,
				Conversion:            &conversion,
			},
		},
		// Environments (function containers)
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "environments.fission.io",
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group: crdGroupName,
				Scope: apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Kind:     "Environment",
					Plural:   "environments",
					Singular: "environment",
				},
				PreserveUnknownFields: boolPtr(false),
				Validation:            environmentValidation,
				Versions:              versions,
				Conversion:            &conversion,
			},
		},
		// HTTP triggers for functions
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "httptriggers.fission.io",
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group: crdGroupName,
				Scope: apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Kind:     "HTTPTrigger",
					Plural:   "httptriggers",
					Singular: "httptrigger",
				},
				Versions:   versions,
				Conversion: &conversion,
			},
		},
		// Kubernetes watch triggers for functions
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "kuberneteswatchtriggers.fission.io",
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group: crdGroupName,
				Scope: apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Kind:     "KubernetesWatchTrigger",
					Plural:   "kuberneteswatchtriggers",
					Singular: "kuberneteswatchtrigger",
				},
				Versions:   versions,
				Conversion: &conversion,
			},
		},
		// Time-based triggers for functions
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "timetriggers.fission.io",
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group: crdGroupName,
				Scope: apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Kind:     "TimeTrigger",
					Plural:   "timetriggers",
					Singular: "timetrigger",
				},
				Versions:   versions,
				Conversion: &conversion,
			},
		},
		// Message queue triggers for functions
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "messagequeuetriggers.fission.io",
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group: crdGroupName,
				Scope: apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Kind:     "MessageQueueTrigger",
					Plural:   "messagequeuetriggers",
					Singular: "messagequeuetrigger",
				},
				Versions:   versions,
				Conversion: &conversion,
			},
		},
		// Packages: archives containing source or binaries for one or more functions
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "packages.fission.io",
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group: crdGroupName,
				Scope: apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Kind:     "Package",
					Plural:   "packages",
					Singular: "package",
				},
				PreserveUnknownFields: boolPtr(false),
				Validation:            packageValidation,
				Versions:              versions,
				Conversion:            &conversion,
			},
		},
		// CanaryConfig: configuration for canary deployment of functions
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "canaryconfigs.fission.io",
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group:   crdGroupName,
				Version: crdVersion,
				Scope:   apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Kind:     "CanaryConfig",
					Plural:   "canaryconfigs",
					Singular: "canaryconfig",
				},
				Conversion: &conversion,
			},
		},
	}
	for _, crd := range crds {
		err := ensureCRD(logger, clientset, &crd)
		if err != nil {
			return err
		}
	}
	return nil
}
