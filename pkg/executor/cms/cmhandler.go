// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package cms

import (
	"context"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

// getConfigmapRelatedFuncs returns functions related to configmap in the same namespace
func getConfigmapRelatedFuncs(ctx context.Context, m *metav1.ObjectMeta, fissionClient versioned.Interface) ([]fv1.Function, error) {
	funcList, err := fissionClient.CoreV1().Functions(m.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	// In future a cache that populates at start and is updated on changes might be better solution
	relatedFunctions := make([]fv1.Function, 0)
	for _, f := range funcList.Items {
		if functionReferencesConfigMap(&f, m) {
			relatedFunctions = append(relatedFunctions, f)
		}
	}
	return relatedFunctions, nil
}

// functionReferencesConfigMap is the ConfigMap counterpart of
// functionReferencesSecret: Spec.ConfigMaps plus the RFC-0030 env fields.
func functionReferencesConfigMap(f *fv1.Function, m *metav1.ObjectMeta) bool {
	for _, cm := range f.Spec.ConfigMaps {
		if (cm.Name == m.Name) && (cm.Namespace == m.Namespace) {
			return true
		}
	}
	if f.Namespace != m.Namespace {
		return false
	}
	return slices.Contains(f.Spec.EnvConfigMapNames(), m.Name)
}
