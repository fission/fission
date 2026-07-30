// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FunctionOwnerRef is the ownerRef an RFC-0025 object (FunctionVersion,
// FunctionAlias) carries back to the Function it belongs to, so the garbage
// collector removes it along with that Function; Controller and
// BlockOwnerDeletion are left unset because no controller adopts these
// objects and Function deletion must never block on them.
func FunctionOwnerRef(fn *Function) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: SchemeGroupVersion.String(),
		Kind:       "Function",
		Name:       fn.Name,
		UID:        fn.UID,
	}
}
