// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package cms

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

// getSecretRelatedFuncs returns functions that are related to the secret in the same namespace
func getSecretRelatedFuncs(ctx context.Context, logger logr.Logger, m *metav1.ObjectMeta, fissionClient versioned.Interface) ([]fv1.Function, error) {
	funcList, err := fissionClient.CoreV1().Functions(m.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	// In future a cache that populates at start and is updated on changes might be better solution
	relatedFunctions := make([]fv1.Function, 0)
	for _, f := range funcList.Items {
		for _, secret := range f.Spec.Secrets {
			if (secret.Name == m.Name) && (secret.Namespace == m.Namespace) {
				relatedFunctions = append(relatedFunctions, f)
				break
			}
		}
	}
	return relatedFunctions, nil
}
