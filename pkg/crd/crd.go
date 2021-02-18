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

	functionVersions := make([]apiextensionsv1beta1.CustomResourceDefinitionVersion, 0)
	functionVersion1 := apiextensionsv1beta1.CustomResourceDefinitionVersion{
		Name:    crdVersion,
		Served:  true,
		Storage: false,
		Schema:  functionValidation,
	}

	functionVersion2 := apiextensionsv1beta1.CustomResourceDefinitionVersion{
		Name:    crdVersion2,
		Served:  true,
		Storage: true,
		Schema:  functionValidationV2,
	}

	version1 := apiextensionsv1beta1.CustomResourceDefinitionVersion{
		Name:    crdVersion,
		Served:  true,
		Storage: false,
	}

	version2 := apiextensionsv1beta1.CustomResourceDefinitionVersion{
		Name:    crdVersion2,
		Served:  true,
		Storage: true,
	}

	// path := "/crdconvert"
	versions = append(versions, version1)
	versions = append(versions, version2)

	functionVersions = append(functionVersions, functionVersion1)
	functionVersions = append(functionVersions, functionVersion2)

	// serviceref := apiextensionsv1beta1.ServiceReference{
	// 	Namespace: "default",
	// 	Name:      "webhook",
	// 	Path:      &path,
	// }

	// cabundle := "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSURLekNDQWhPZ0F3SUJBZ0lSQU9zcEtDOXByRGJpOXNuY0M4d1hWTHN3RFFZSktvWklodmNOQVFFTEJRQXcKTHpFdE1Dc0dBMVVFQXhNa1ltTTRPREExTW1ZdE1EUm1NQzAwTm1NNUxUa3pNVGd0TkRSaFlqYzNNMlF6TTJKagpNQjRYRFRJeE1ESXhPREEzTURneE1Wb1hEVEkyTURJeE56QTRNRGd4TVZvd0x6RXRNQ3NHQTFVRUF4TWtZbU00Ck9EQTFNbVl0TURSbU1DMDBObU01TFRrek1UZ3RORFJoWWpjM00yUXpNMkpqTUlJQklqQU5CZ2txaGtpRzl3MEIKQVFFRkFBT0NBUThBTUlJQkNnS0NBUUVBNW9oUFV3ZGljYVFMWlhGN1BLOG96SHhXTWl1Ty93WWhHVVFreU9xcgpBenN6R0RBTVlNN2hFWEJiS3NHVUVBa3JLc3FHb2c4QUxLZDJLYUN4OEQ4bks4YzBrUFNQQWJCMmk1SXNlN3NzCmJyTU5aMEJHenREVjM5SHd2V3J4OEZLWHNUY1JhbG9hbmZVMnV1YTdnWFRXb2t0TjU1cXhNMHZOamcwMmIxQW8KWWtCNzBHa2FYaVd5UlRieXdtZUdyVzZHSmkyN0ZGTkJicHFuMUc5VmxscS93U0plMmtDMGp0ZGRwL3JEaEVxQgppRXNTVFQvMTBnMWk2ZFphaUxhb2pYZ0EvdkFHTlZQY3BkWW9acjJQdkFtUWg2cEFqWWp1NHFBN2JmRHdxVE5aCm1aZ05lWXdVOFVkMGtleHo0cVNuS2c1TzZvZWsyejFRcmg4MG10dHdzVXhhcHdJREFRQUJvMEl3UURBT0JnTlYKSFE4QkFmOEVCQU1DQWdRd0R3WURWUjBUQVFIL0JBVXdBd0VCL3pBZEJnTlZIUTRFRmdRVVpKTm42T1lDYUdiNApVSkFlS2FrVEZORW9YTjh3RFFZSktvWklodmNOQVFFTEJRQURnZ0VCQU1ROW42VjF2aEw4NGdvYkR5R3laSFBQCmVybGZ3S214TU1iQTJIamZzamh4UTRqdXhONmFJaFFTeUZPUmFvdFp5RGsxRFZmQlRXcUJtcWR3WnFyUFkyVisKYUh2YkNUVHpqQk5GY2dEWVNJcFlxUzEwZFpTSEJlVXEwNFNQSi9haWhPdmdleWpaaEprMmFPT3BVcFNJeUlOYQo5SFhFMlVVQmVJZU9nc2tOQ1FsN3htaWxhdkRycTVSWmtRWTVIb1hVYTc3ZmViOWhHOTNGdVpjZ2xxZXYwZUtTCngxTm1VRjJ3WDNZcTBFTHRIcEJCeUh6UG4wQWRzWXFSZzFQOVcvTnZIM21QckxLVlUrMkNkMW43bUt3SXY1dXgKZ0dBNG1zdGd3QnBrczlLOUlWTkh2OWZ3aXNOZDdHU2U0R0xmc29YME83eVQzclVRV3IxREk4ckNGY3hNMnlnPQotLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tCg=="
	// webhookconfig := apiextensionsv1beta1.WebhookClientConfig{
	// 	Service:  &serviceref,
	// 	CABundle: []byte(cabundle),
	// // }
	// webhookConversion := apiextensionsv1beta1.CustomResourceConversion{
	// 	Strategy:                 apiextensionsv1beta1.ConversionStrategyType("Webhook"),
	// 	WebhookClientConfig:      &webhookconfig,
	// 	ConversionReviewVersions: []string{"v1"},
	// }

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
				Versions:              functionVersions,
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
