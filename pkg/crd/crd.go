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
)

var (
	functionValidation = &apiextensionsv1beta1.CustomResourceValidation{
		OpenAPIV3Schema: &apiextensionsv1beta1.JSONSchemaProps{
			Type:        "object",
			Description: "Function in fission is something that Fission executes. It’s usually a module with one entry point, and that entry point is a function with a certain interface.",
			Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
				"spec": {
					Type:        "object",
					Description: "Specification of the desired behaviour of the Function",
					Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
						"environment": {
							Type:        "object",
							Description: "Environments are the language-specific parts of Fission. An Environment contains just enough software to build and run a Fission Function.",
						},
						"package": {
							Type:        "object",
							Description: "A Package is a Fission object containing a Deployment Archive and a Source Archive (if any). A Package also references a certain environment.",
						},
						"secrets": {
							Type:        "array",
							Description: "Reference to a list of secrets.",
							Items: &apiextensionsv1beta1.JSONSchemaPropsOrArray{
								Schema: &apiextensionsv1beta1.JSONSchemaProps{
									Type: "object",
								},
							},
						},
						"configmaps": {
							Type:        "array",
							Description: "Reference to a list of configmaps.",
							Items: &apiextensionsv1beta1.JSONSchemaPropsOrArray{
								Schema: &apiextensionsv1beta1.JSONSchemaProps{
									Type: "object",
								},
							},
						},
						"resources": {
							Type:        "object",
							Description: "ResourceRequirements describes the compute resource requirements. This is only for newdeploy to set up resource limitation when creating deployment for a function.",
						},
						"invokeStrategy": {
							Type:        "object",
							Description: "InvokeStrategy is a set of controls which affect how function executes",
						},
						"functionTimeout": {
							Type:        "integer",
							Description: " FunctionTimeout provides a maximum amount of duration within which a request for a particular function execution should be complete.\nThis is optional. If not specified default value will be taken as 60s",
						},
						"idletimeout": {
							Type:        "object",
							Description: "IdleTimeout specifies the length of time that a function is idle before the function pod(s) are eligible for deletion. If no traffic to the function is detected within the idle timeout, the executor will then recycle the function pod(s) to release resources.",
						},
					},
				},
			},
		},
	}
	environmentValidation = &apiextensionsv1beta1.CustomResourceValidation{
		OpenAPIV3Schema: &apiextensionsv1beta1.JSONSchemaProps{
			Type:        "object",
			Description: "Environments are the language-specific parts of Fission. An Environment contains just enough software to build and run a Fission Function.",
			Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
				"spec": {
					Type:        "object",
					Description: "Specification of the desired behaviour of the Environment",
					Properties: map[string]apiextensionsv1beta1.JSONSchemaProps{
						"version": {
							Type:        "integer",
							Description: "Version is the Environment API version",
						},
						"runtime": {
							Type:        "object",
							Description: "Runtime is configuration for running function, like container image etc.",
						},
						"builder": {
							Type:        "object",
							Description: "Builder is configuration for builder manager to launch environment builder to build source code into deployable binary.",
						},
						"allowedFunctionsPerContainer": {
							Type:        "object",
							Description: "Allowed functions per container. Allowed Values: single, multiple",
						},
						"allowAccessToExternalNetwork": {
							Type:        "boolean",
							Description: "To enable accessibility of external network for builder/function pod, set to 'true'.",
						},
						"resources": {
							Type:        "object",
							Description: "The request and limit CPU/MEM resource setting for poolmanager to set up pods in the pre-warm pool.",
						},
						"poolsize": {
							Type:        "integer",
							Description: "The initial pool size for environment",
						},
						"terminationGracePeriod": {
							Type:        "integer",
							Description: "The grace time for pod to perform connection draining before termination. The unit is in seconds.",
						},
						"keeparchive": {
							Type:        "boolean",
							Description: "KeepArchive is used by fetcher to determine if the extracted archive or unarchived file should be placed, which is then used by specialize handler.",
						},
						"imagepullsecret": {
							Type:        "string",
							Description: "ImagePullSecret is the secret for Kubernetes to pull an image from a private registry.",
						},
					},
				},
			},
		},
	}
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

func boolPtr(b bool) *bool {
	return &b
}

// Ensure CRDs
func EnsureFissionCRDs(logger *zap.Logger, clientset *apiextensionsclient.Clientset) error {
	crds := []apiextensionsv1beta1.CustomResourceDefinition{
		// Functions
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "functions.fission.io",
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group:   crdGroupName,
				Version: crdVersion,
				Scope:   apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Kind:     "Function",
					Plural:   "functions",
					Singular: "function",
				},
				PreserveUnknownFields: boolPtr(false),
				Validation:            functionValidation,
			},
		},
		// Environments (function containers)
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "environments.fission.io",
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group:   crdGroupName,
				Version: crdVersion,
				Scope:   apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Kind:     "Environment",
					Plural:   "environments",
					Singular: "environment",
				},
				PreserveUnknownFields: boolPtr(false),
				Validation:            environmentValidation,
			},
		},
		// HTTP triggers for functions
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "httptriggers.fission.io",
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group:   crdGroupName,
				Version: crdVersion,
				Scope:   apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Kind:     "HTTPTrigger",
					Plural:   "httptriggers",
					Singular: "httptrigger",
				},
			},
		},
		// Kubernetes watch triggers for functions
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "kuberneteswatchtriggers.fission.io",
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group:   crdGroupName,
				Version: crdVersion,
				Scope:   apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Kind:     "KubernetesWatchTrigger",
					Plural:   "kuberneteswatchtriggers",
					Singular: "kuberneteswatchtrigger",
				},
			},
		},
		// Time-based triggers for functions
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "timetriggers.fission.io",
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group:   crdGroupName,
				Version: crdVersion,
				Scope:   apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Kind:     "TimeTrigger",
					Plural:   "timetriggers",
					Singular: "timetrigger",
				},
			},
		},
		// Message queue triggers for functions
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "messagequeuetriggers.fission.io",
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group:   crdGroupName,
				Version: crdVersion,
				Scope:   apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Kind:     "MessageQueueTrigger",
					Plural:   "messagequeuetriggers",
					Singular: "messagequeuetrigger",
				},
			},
		},
		// Packages: archives containing source or binaries for one or more functions
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "packages.fission.io",
			},
			Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
				Group:   crdGroupName,
				Version: crdVersion,
				Scope:   apiextensionsv1beta1.NamespaceScoped,
				Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
					Kind:     "Package",
					Plural:   "packages",
					Singular: "package",
				},
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
