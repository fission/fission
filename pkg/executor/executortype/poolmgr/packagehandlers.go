/*
Copyright 2018 The Fission Authors.

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

package poolmgr

import (
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
)

func PackageEventHandlers(logger *zap.Logger, kubernetesClient *kubernetes.Clientset, fissionfnNamespace string) k8sCache.ResourceEventHandlerFuncs {
	return k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pkg := obj.(*fv1.Package)
			logger.Debug("list watch for package reported a new package addition",
				zap.String("package_name", pkg.ObjectMeta.Name),
				zap.String("package_namespace", pkg.ObjectMeta.Namespace))

			// create or update role-binding for fetcher sa in env ns to be able to get the pkg contents from pkg namespace
			envNs := fissionfnNamespace
			if pkg.Spec.Environment.Namespace != metav1.NamespaceDefault {
				envNs = pkg.Spec.Environment.Namespace
			}

			// here, we return if we hit an error during rolebinding setup. this is because this rolebinding is mandatory for
			// every function's package to be loaded into its env. without that, there's no point to move forward.
			err := utils.SetupRoleBinding(logger, kubernetesClient, fv1.PackageGetterRB, pkg.ObjectMeta.Namespace, fv1.PackageGetterCR, fv1.ClusterRole, fv1.FissionFetcherSA, envNs)
			if err != nil {
				logger.Error("error creating rolebinding for package",
					zap.Error(err),
					zap.String("role_binding", fv1.PackageGetterRB),
					zap.String("package_name", pkg.ObjectMeta.Name),
					zap.String("package_namespace", pkg.ObjectMeta.Namespace))
				return
			}

			logger.Debug("successfully set up rolebinding for fetcher service account",
				zap.String("service_account", fv1.FissionFetcherSA),
				zap.String("service_account_namespace", envNs),
				zap.String("package_name", pkg.ObjectMeta.Name),
				zap.String("package_namespace", pkg.ObjectMeta.Namespace))
		},

		UpdateFunc: func(oldObj, newObj interface{}) {
			oldPkg := oldObj.(*fv1.Package)
			newPkg := newObj.(*fv1.Package)

			if oldPkg.ObjectMeta.ResourceVersion == newPkg.ObjectMeta.ResourceVersion {
				return
			}

			// if a pkg's env reference gets updated and the newly referenced env is in a different ns,
			// we need to update the role-binding in pkg ns to grant permissions to the fetcher-sa in env ns
			// to do a get on pkg
			if oldPkg.Spec.Environment.Namespace != newPkg.Spec.Environment.Namespace {
				envNs := fissionfnNamespace
				if newPkg.Spec.Environment.Namespace != metav1.NamespaceDefault {
					envNs = newPkg.Spec.Environment.Namespace
				}

				err := utils.SetupRoleBinding(logger, kubernetesClient, fv1.PackageGetterRB,
					newPkg.ObjectMeta.Namespace, fv1.PackageGetterCR, fv1.ClusterRole,
					fv1.FissionFetcherSA, envNs)
				if err != nil {
					logger.Error("error updating rolebinding for package",
						zap.Error(err),
						zap.String("role_binding", fv1.PackageGetterRB),
						zap.String("package_name", newPkg.ObjectMeta.Name),
						zap.String("package_namespace", newPkg.ObjectMeta.Namespace))
					return
				}

				logger.Debug("successfully updated rolebinding for fetcher service account",
					zap.String("service_account", fv1.FissionFetcherSA),
					zap.String("service_account_namespace", envNs),
					zap.String("package_name", newPkg.ObjectMeta.Name),
					zap.String("package_namespace", newPkg.ObjectMeta.Namespace))
			}
		},
	}
}
