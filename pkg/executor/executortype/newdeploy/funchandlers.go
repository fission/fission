// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import (
	"context"

	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func (deploy *NewDeploy) FunctionEventHandlers(ctx context.Context) k8sCache.ResourceEventHandlerFuncs {
	return k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			// TODO: A workaround to process items in parallel. We should use workqueue ("k8s.io/client-go/util/workqueue")
			// and worker pattern to process items instead of moving process to another goroutine.
			// example: https://github.com/kubernetes/kubernetes/blob/master/pkg/controller/job/job_controller.go
			go func() {
				fn := obj.(*fv1.Function)
				deploy.logger.V(1).Info("create deployment for function", "fn", fn.ObjectMeta, "fnspec", fn.Spec)
				_, err := deploy.createFunction(ctx, fn)
				if err != nil {
					deploy.logger.Error(err, "error eager creating function", "function", fn)
				}
				deploy.logger.V(1).Info("end create deployment for function", "fn", fn.ObjectMeta, "fnspec", fn.Spec)
			}()
		},
		DeleteFunc: func(obj any) {
			fn := obj.(*fv1.Function)
			go func() {
				err := deploy.deleteFunction(ctx, fn)
				if err != nil {
					deploy.logger.Error(err, "error deleting function", "function", fn)
				}
			}()
		},
		UpdateFunc: func(oldObj any, newObj any) {
			oldFn := oldObj.(*fv1.Function)
			newFn := newObj.(*fv1.Function)
			go func() {
				err := deploy.updateFunction(ctx, oldFn, newFn)
				if err != nil {
					deploy.logger.Error(err, "error updating function", "old_function", oldFn,
						"new_function", newFn)
				}
			}()
		},
	}
}
