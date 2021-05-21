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
	"context"
	"time"

	"go.uber.org/zap"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var functionCrdVersions, environmentCrdVersions, packageCrdVersions, httpTriggerCrdVersions, timeTriggerCrdVersions, k8swatchTriggerCrdVersions, mqTriggerCrdVersions, canaryconfigCrdVersions []apiextensionsv1.CustomResourceDefinitionVersion

const (
	crdGroupName = "fission.io"
	crdVersion   = "v1"
)

// ensureCRD checks if the given CRD type exists, and creates it if
// needed. (Note that this creates the CRD type; it doesn't create any
// _instances_ of that type.)
func ensureCRD(logger *zap.Logger, clientset *apiextensionsclient.Clientset, crd string) (err error) {
	maxRetries := 5

	for i := 0; i < maxRetries; i++ {
		_, err = clientset.ApiextensionsV1().CustomResourceDefinitions().Get(context.Background(), crd, metav1.GetOptions{})
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

// EnsureFissionCRDs defines all the CRDs that need to be ensured
func EnsureFissionCRDs(logger *zap.Logger, clientset *apiextensionsclient.Clientset) error {
	crds := []string{"canaryconfigs.fission.io", "packages.fission.io", "environments.fission.io", "functions.fission.io", "httptriggers.fission.io", "kuberneteswatchtriggers.fission.io", "messagequeuetriggers.fission.io", "timetriggers.fission.io"}

	for _, crd := range crds {
		err := ensureCRD(logger, clientset, crd)
		if err != nil {
			return err
		}
	}
	return nil
}
