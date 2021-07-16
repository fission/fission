/*
Copyright 2020 The Fission Authors.

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

package container

import (
	"go.uber.org/zap"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func (caaf *Container) FuncInformerHandler() k8sCache.ResourceEventHandlerFuncs {
	return k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			fn := obj.(*fv1.Function)
			fnExecutorType := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
			if fnExecutorType != "" && fnExecutorType != fv1.ExecutorTypeContainer {
				return
			}
			// TODO: A workaround to process items in parallel. We should use workqueue ("k8s.io/client-go/util/workqueue")
			// and worker pattern to process items instead of moving process to another goroutine.
			// example: https://github.com/kubernetes/kubernetes/blob/master/pkg/controller/job/job_controller.go
			go func() {
				log := caaf.logger.With(zap.String("function_name", fn.ObjectMeta.Name), zap.String("function_namespace", fn.ObjectMeta.Namespace))
				log.Debug("start function create handler")
				_, err := caaf.createFunction(fn)
				if err != nil {
					log.Error("error eager creating function", zap.Error(err))
				}
				log.Debug("end function create handler")
			}()
		},
		DeleteFunc: func(obj interface{}) {
			fn := obj.(*fv1.Function)
			fnExecutorType := fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
			if fnExecutorType != "" && fnExecutorType != fv1.ExecutorTypeContainer {
				return
			}
			go func() {
				log := caaf.logger.With(zap.String("function_name", fn.ObjectMeta.Name), zap.String("function_namespace", fn.ObjectMeta.Namespace))
				log.Debug("start function delete handler")
				err := caaf.deleteFunction(fn)
				if err != nil {
					log.Error("error deleting function", zap.Error(err))
				}
				log.Debug("end function delete handler")
			}()
		},
		UpdateFunc: func(oldObj interface{}, newObj interface{}) {
			oldFn := oldObj.(*fv1.Function)
			newFn := newObj.(*fv1.Function)
			fnExecutorType := oldFn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType
			if fnExecutorType != "" && fnExecutorType != fv1.ExecutorTypeContainer {
				return
			}
			go func() {
				log := caaf.logger.With(zap.String("function_name", newFn.ObjectMeta.Name),
					zap.String("function_namespace", newFn.ObjectMeta.Namespace),
					zap.String("old_function_name", oldFn.ObjectMeta.Name))
				log.Debug("start function update handler")
				err := caaf.updateFunction(oldFn, newFn)
				if err != nil {
					log.Error("error updating function",
						zap.Error(err))
				}
				log.Debug("end function update handler")

			}()
		},
	}
}
