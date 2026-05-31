// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import ctrl "sigs.k8s.io/controller-runtime"

// RegisterReconcilers is a no-op: newdeploy still drives off informer event
// handlers (Function + Environment). Its reconciler migration needs more care
// around the function/environment update race and is deferred to a dedicated PR.
func (deploy *NewDeploy) RegisterReconcilers(mgr ctrl.Manager) error {
	return nil
}
