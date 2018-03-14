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
	"reflect"
	"time"

	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	k8sCache "k8s.io/client-go/tools/cache"

	"github.com/fission/fission"
	"github.com/fission/fission/crd"
)

// TODO : It may make sense to make each of add, update, delete funcs run as separate go routines.
func (gpm *GenericPoolManager) makePkgController(fissionClient *crd.FissionClient,
	kubernetesClient *kubernetes.Clientset, fissionfnNamespace string) (k8sCache.Store, k8sCache.Controller) {

	resyncPeriod := 30 * time.Second
	lw := k8sCache.NewListWatchFromClient(fissionClient.GetCrdClient(), "packages", metav1.NamespaceAll, fields.Everything())
	pkgStore, controller := k8sCache.NewInformer(lw, &crd.Package{}, resyncPeriod,
		k8sCache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				pkg := obj.(*crd.Package)
				log.Printf("List watch for package reported a new package addition: %s.%s", pkg.Metadata.Name, pkg.Metadata.Namespace)

				// create or update role-binding for fetcher sa in env ns to be able to get the pkg contents from pkg namespace
				envNs := fissionfnNamespace
				if pkg.Spec.Environment.Namespace != metav1.NamespaceDefault {
					envNs = pkg.Spec.Environment.Namespace
				}

				// here, we return if we hit an error during rolebinding setup. this is because this rolebinding is mandatory for
				// every function's package to be loaded into its env. without that, there's no point to move forward.
				err := fission.SetupRoleBinding(kubernetesClient, fission.PackageGetterRB, pkg.Metadata.Namespace, fission.PackageGetterCR, fission.ClusterRole, fission.FissionFetcherSA, envNs)
				if err != nil {
					log.Printf("Error creating %s for package: %s.%s, err: %v", fission.PackageGetterRB, pkg.Metadata.Name, pkg.Metadata.Namespace, err)
					return
				}

				log.Printf("Successfully set up rolebinding for fetcher SA: %s.%s, in packages's ns : %s, for pkg : %s", fission.FissionFetcherSA, envNs, pkg.Metadata.Namespace, pkg.Metadata.Name)
			},

			DeleteFunc: func(obj interface{}) {
				pkg := obj.(*crd.Package)
				gpm.cleanupPkgWatcherRolebinding(fissionClient, pkg, kubernetesClient, fissionfnNamespace)
			},

			UpdateFunc: func(oldObj, newObj interface{}) {
				oldPkg := oldObj.(*crd.Package)
				newPkg := newObj.(*crd.Package)

				// if a pkg's env reference gets updated and the newly referenced env is in a different ns,
				// we need to update the role-binding in pkg ns to grant permissions to the fetcher-sa in env ns
				// to do a get on pkg
				if oldPkg.Spec.Environment.Namespace != newPkg.Spec.Environment.Namespace {
					envNs := fissionfnNamespace
					if newPkg.Spec.Environment.Namespace != metav1.NamespaceDefault {
						envNs = newPkg.Spec.Environment.Namespace
					}

					err := fission.SetupRoleBinding(kubernetesClient, fission.PackageGetterRB,
						newPkg.Metadata.Namespace, fission.PackageGetterCR, fission.ClusterRole,
						fission.FissionFetcherSA, envNs)
					if err != nil {
						log.Printf("Error : %v updating %s for package: %s.%s", err, fission.PackageGetterRB, newPkg.Metadata.Name, newPkg.Metadata.Namespace)
						return
					}
					log.Printf("Updated rolebinding for fetcher SA: %s.%s, in packages's ns : %s, for pkg : %s", fission.FissionFetcherSA, envNs, newPkg.Metadata.Namespace, newPkg.Metadata.Name)

					// also remove old sa : fission-fetcher.oldEnvNs from rolebinding, if there are no pkgs in this ns referencing the same env.
					gpm.cleanupPkgWatcherRolebinding(fissionClient, oldPkg, kubernetesClient, fissionfnNamespace)
				}
			},
		})

	return pkgStore, controller
}

func (gpm *GenericPoolManager) cleanupPkgWatcherRolebinding(fissionClient *crd.FissionClient, pkg *crd.Package, kubernetesClient *kubernetes.Clientset, fissionfnNamespace string) {
	objList := gpm.pkgStore.List()

	// there's no pkg in this ns, safe to delete the RoleBinding obj.
	if len(objList) == 0 {
		err := fission.DeleteRoleBinding(kubernetesClient, fission.PackageGetterRB, pkg.Metadata.Namespace)
		if err != nil {
			log.Printf("Error deleting role binding: %s.%s", fission.PackageGetterRB, pkg.Metadata.Namespace)
			return
		}
		log.Printf("Deleted role binding: %s.%s", fission.PackageGetterRB, pkg.Metadata.Namespace)
		return
	}

	// check all the packages sharing the env referenced in this pkg.
	// if there's none, then remove the fetcher sa for this env from the rolebinding
	for _, item := range objList {
		pkgItem, ok := item.(*crd.Package)
		if !ok {
			log.Printf("Asserting failed for packageItem, type of item : %v", reflect.TypeOf(item))
			return
		}
		if pkg.Spec.Environment.Name == pkgItem.Spec.Environment.Name && pkg.Spec.Environment.Namespace == pkgItem.Spec.Environment.Namespace &&
			pkgItem.Metadata.Name != pkg.Metadata.Name {
			return
		}
	}
	// us landing here implies no other pkg in this ns references this env, so safely remove sa from role-binding
	envNs := fissionfnNamespace
	if pkg.Spec.Environment.Namespace != metav1.NamespaceDefault {
		envNs = pkg.Spec.Environment.Namespace
	}
	err := fission.RemoveSAFromRoleBindingWithRetries(kubernetesClient, fission.PackageGetterRB, pkg.Metadata.Namespace, fission.FissionFetcherSA, envNs)
	if err != nil {
		log.Printf("Error removing sa: %s.%s from role binding: %s.%s", fission.FissionFetcherSA, envNs, fission.PackageGetterRB, pkg.Metadata.Namespace)
		return
	}
	log.Printf("Removed sa : %s.%s from role binding: %s.%s", fission.FissionFetcherSA, envNs, fission.PackageGetterRB, pkg.Metadata.Namespace)
}
