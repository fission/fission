// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import ctrl "sigs.k8s.io/controller-runtime"

// RegisterReconcilers is a no-op: the pool manager still drives off informer
// event handlers (poolpodcontroller + readyPodController). It migrates to
// controller-runtime reconcilers in a later WS3 step.
func (gpm *GenericPoolManager) RegisterReconcilers(mgr ctrl.Manager) error {
	return nil
}
