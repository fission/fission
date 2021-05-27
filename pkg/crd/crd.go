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
	"fmt"

	"github.com/hashicorp/go-multierror"
	"go.uber.org/zap"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnsureFissionCRDs checks if all Fission CRDs are present
func EnsureFissionCRDs(logger *zap.Logger, clientset *apiextensionsclient.Clientset) error {
	crdsExpected := []string{
		"canaryconfigs.fission.io",
		"environments.fission.io",
		"functions.fission.io",
		"httptriggers.fission.io",
		"kuberneteswatchtriggers.fission.io",
		"messagequeuetriggers.fission.io",
		"packages.fission.io",
		"timetriggers.fission.io",
	}
	errs := &multierror.Error{}
	for _, crdName := range crdsExpected {
		crd, err := clientset.ApiextensionsV1().CustomResourceDefinitions().Get(context.TODO(), crdName, metav1.GetOptions{})
		if err != nil {
			multierror.Append(errs, fmt.Errorf("CRD %s not found: %s", crdName, err))
		}
		if crd == nil {
			multierror.Append(errs, fmt.Errorf("CRD %s not found", crdName))
		}
	}
	return errs.ErrorOrNil()
}
