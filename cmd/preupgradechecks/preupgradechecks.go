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

package main

import (
	"fmt"
	"strings"

	multierror "github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
)

type (
	PreUpgradeTaskClient struct {
		logger        *zap.Logger
		fissionClient *crd.FissionClient
		k8sClient     *kubernetes.Clientset
		apiExtClient  *apiextensionsclient.Clientset
		fnPodNs       string
		envBuilderNs  string
	}
)

const (
	maxRetries  = 5
	FunctionCRD = "functions.fission.io"
)

func makePreUpgradeTaskClient(logger *zap.Logger, fnPodNs, envBuilderNs string) (*PreUpgradeTaskClient, error) {
	fissionClient, k8sClient, apiExtClient, _, err := crd.MakeFissionClient()
	if err != nil {
		return nil, errors.Wrap(err, "error making fission client")
	}

	return &PreUpgradeTaskClient{
		logger:        logger.Named("pre_upgrade_task_client"),
		fissionClient: fissionClient,
		k8sClient:     k8sClient,
		fnPodNs:       fnPodNs,
		envBuilderNs:  envBuilderNs,
		apiExtClient:  apiExtClient,
	}, nil
}

// IsFissionReInstall checks if there is at least one fission CRD, i.e. function in this case, on this cluster.
// We need this to find out if fission had been previously installed on this cluster
func (client *PreUpgradeTaskClient) IsFissionReInstall() bool {
	var found bool
	for i := 0; i < maxRetries; i++ {
		found = true
		_, err := client.apiExtClient.ApiextensionsV1beta1().CustomResourceDefinitions().Get(FunctionCRD, metav1.GetOptions{})
		if err != nil && k8serrors.IsNotFound(err) {
			found = false
		}
	}

	return found
}

// LatestSchemaApplied ensures that the end user has applied the latest CRDs generated to the cluster.
// For future reference: whenever a new field is added, we need to check for that field's existence in this function
func (client *PreUpgradeTaskClient) LatestSchemaApplied() error {
	client.logger.Info("Checking if user has applied the latest CRDs")
	crd, err := client.apiExtClient.ApiextensionsV1().CustomResourceDefinitions().Get("functions.fission.io", metav1.GetOptions{})
	if err != nil {
		client.logger.Error("Could not get the CRD")
		return err
	}
	// Any new field added in Function spec can be checked here provided the substring matches the description in CRD Validation of the field
	if strings.Contains(crd.Spec.String(), "RequestsPerPod") {
		return nil
	}
	client.logger.Error("Could not find the newer fields")
	return errors.New("Apply the newer CRDs before upgrading")
}

// VerifyFunctionSpecReferences verifies that a function references secrets, configmaps, pkgs in its own namespace and
// outputs a list of functions that don't adhere to this requirement.
func (client *PreUpgradeTaskClient) VerifyFunctionSpecReferences() {
	client.logger.Info("verifying function spec references for all functions in the cluster")

	var err error
	var fList *fv1.FunctionList

	for i := 0; i < maxRetries; i++ {
		fList, err = client.fissionClient.CoreV1().Functions(metav1.NamespaceAll).List(metav1.ListOptions{})
		if err == nil {
			break
		}
	}

	if err != nil {
		client.logger.Fatal("error listing functions after max retries",
			zap.Error(err),
			zap.Int("max_retries", maxRetries))
	}

	errs := &multierror.Error{}

	// check that all secrets, configmaps, packages are in the same namespace
	for _, fn := range fList.Items {
		secrets := fn.Spec.Secrets
		for _, secret := range secrets {
			if secret.Namespace != fn.ObjectMeta.Namespace {
				errs = multierror.Append(errs, fmt.Errorf("function : %s.%s cannot reference a secret : %s in namespace : %s", fn.ObjectMeta.Name, fn.ObjectMeta.Namespace, secret.Name, secret.Namespace))
			}
		}

		configmaps := fn.Spec.ConfigMaps
		for _, configmap := range configmaps {
			if configmap.Namespace != fn.ObjectMeta.Namespace {
				errs = multierror.Append(errs, fmt.Errorf("function : %s.%s cannot reference a configmap : %s in namespace : %s", fn.ObjectMeta.Name, fn.ObjectMeta.Namespace, configmap.Name, configmap.Namespace))
			}
		}

		if fn.Spec.Package.PackageRef.Namespace != fn.ObjectMeta.Namespace {
			errs = multierror.Append(errs, fmt.Errorf("function : %s.%s cannot reference a package : %s in namespace : %s", fn.ObjectMeta.Name, fn.ObjectMeta.Namespace, fn.Spec.Package.PackageRef.Name, fn.Spec.Package.PackageRef.Namespace))
		}
	}

	if errs.ErrorOrNil() != nil {
		client.logger.Fatal("installation failed",
			zap.Error(err),
			zap.String("summary", "a function cannot reference secrets, configmaps and packages outside it's own namespace"))
	}

	client.logger.Info("function spec references verified")
}
