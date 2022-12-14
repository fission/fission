/*
Copyright 2021 The Fission Authors.

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
package newdeploy

import (
	"context"
	"time"

	"go.uber.org/zap"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func (deploy *NewDeploy) FunctionEventHandlers(ctx context.Context) k8sCache.ResourceEventHandlerFuncs {
	return k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			// TODO: A workaround to process items in parallel. We should use workqueue ("k8s.io/client-go/util/workqueue")
			// and worker pattern to process items instead of moving process to another goroutine.
			// example: https://github.com/kubernetes/kubernetes/blob/master/pkg/controller/job/job_controller.go
			go func() {
				fn := obj.(*fv1.Function)
				specializationTimeout := fn.Spec.InvokeStrategy.ExecutionStrategy.SpecializationTimeout
				if specializationTimeout < fv1.DefaultSpecializationTimeOut {
					specializationTimeout = fv1.DefaultSpecializationTimeOut
				}
				ctx2, cancel := context.WithTimeout(ctx, time.Duration(specializationTimeout)*time.Second)
				defer cancel()
				deploy.logger.Debug("create deployment for function", zap.Any("fn", fn.ObjectMeta), zap.Any("fnspec", fn.Spec))
				_, err := deploy.createFunction(ctx2, fn)
				if err != nil {
					deploy.logger.Error("error eager creating function",
						zap.Error(err),
						zap.Any("function", fn))
				}
				deploy.logger.Debug("end create deployment for function", zap.Any("fn", fn.ObjectMeta), zap.Any("fnspec", fn.Spec))
			}()
		},
		DeleteFunc: func(obj interface{}) {
			fn := obj.(*fv1.Function)
			go func() {
				err := deploy.deleteFunction(ctx, fn)
				if err != nil {
					deploy.logger.Error("error deleting function",
						zap.Error(err),
						zap.Any("function", fn))
				}
			}()
		},
		UpdateFunc: func(oldObj interface{}, newObj interface{}) {
			oldFn := oldObj.(*fv1.Function)
			newFn := newObj.(*fv1.Function)
			go func() {
				specializationTimeout := newFn.Spec.InvokeStrategy.ExecutionStrategy.SpecializationTimeout
				if specializationTimeout < fv1.DefaultSpecializationTimeOut {
					specializationTimeout = fv1.DefaultSpecializationTimeOut
				}
				ctx2, cancel := context.WithTimeout(ctx, time.Duration(specializationTimeout)*time.Second)
				defer cancel()
				err := deploy.updateFunction(ctx2, oldFn, newFn)
				if err != nil {
					deploy.logger.Error("error updating function",
						zap.Error(err),
						zap.Any("old_function", oldFn),
						zap.Any("new_function", newFn))
				}
			}()
		},
	}
}
