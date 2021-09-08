/*
Copyright 2017 The Fission Authors.

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

package buildermgr

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	k8sCache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/cache"
	"github.com/fission/fission/pkg/crd"
	finformerv1 "github.com/fission/fission/pkg/generated/informers/externalversions/core/v1"
	flisterv1 "github.com/fission/fission/pkg/generated/listers/core/v1"
	"github.com/fission/fission/pkg/utils"
)

type (
	packageWatcher struct {
		logger           *zap.Logger
		fissionClient    *crd.FissionClient
		k8sClient        *kubernetes.Clientset
		podLister        corelisters.PodLister
		podListerSynced  k8sCache.InformerSynced
		pkgLister        flisterv1.PackageLister
		pkgListerSynced  k8sCache.InformerSynced
		pkgQueue         workqueue.RateLimitingInterface
		builderNamespace string
		storageSvcUrl    string
		buildCache       *cache.Cache
	}
)

func makePackageWatcher(logger *zap.Logger, fissionClient *crd.FissionClient, k8sClientSet *kubernetes.Clientset,
	builderNamespace string, storageSvcUrl string, podInformer coreinformers.PodInformer,
	pkgInformer finformerv1.PackageInformer) *packageWatcher {
	pkgw := &packageWatcher{
		logger:           logger.Named("package_watcher"),
		fissionClient:    fissionClient,
		k8sClient:        k8sClientSet,
		builderNamespace: builderNamespace,
		storageSvcUrl:    storageSvcUrl,
		buildCache:       cache.MakeCache(0, 0),
		pkgQueue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "package"),
	}
	pkgw.podLister = podInformer.Lister()
	pkgw.podListerSynced = podInformer.Informer().HasSynced
	pkgw.pkgLister = pkgInformer.Lister()
	pkgw.pkgListerSynced = pkgInformer.Informer().HasSynced
	pkgInformer.Informer().AddEventHandler(pkgw.packageInformerHandler())
	return pkgw
}

func (pkgw *packageWatcher) packageInformerHandler() k8sCache.ResourceEventHandler {
	return k8sCache.ResourceEventHandlerFuncs{
		AddFunc:    pkgw.handlePackageAdd,
		UpdateFunc: pkgw.handlePackageUpdate,
	}
}

func (pkgw *packageWatcher) handlePackageAdd(obj interface{}) {
	key, err := k8sCache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		pkgw.logger.Error("error retrieving key from object in packageWatcher", zap.Any("obj", obj))
		return
	}
	pkgw.logger.Debug("enqueue pkg add", zap.String("key", key))
	pkgw.pkgQueue.Add(key)
}

func (pkgw *packageWatcher) handlePackageUpdate(obj, newObj interface{}) {
	key, err := k8sCache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		pkgw.logger.Error("error retrieving key from object in packageWatcher", zap.Any("obj", obj))
		return
	}
	pkgw.logger.Debug("enqueue pkg update", zap.String("key", key))
	pkgw.pkgQueue.Add(key)
}

func (pkgw *packageWatcher) workerRun(name string, processFunc func() bool) func() {
	return func() {
		pkgw.logger.Debug("Starting worker with func", zap.String("name", name))
		for {
			if quit := processFunc(); quit {
				pkgw.logger.Info("Shutting down worker", zap.String("name", name))
				return
			}
		}
	}
}

func (pkgw *packageWatcher) pkgQueueProcessFunc() bool {
	maxRetries := 3
	handlePkg := func(ctx context.Context, pkg *fv1.Package) error {
		log := pkgw.logger.With(zap.String("pkg", pkg.ObjectMeta.Name), zap.String("namespace", pkg.ObjectMeta.Namespace))
		log.Debug("pkg reconsile request processing")
		if len(pkg.Status.BuildStatus) == 0 {
			_, err := setInitialBuildStatus(ctx, pkgw.fissionClient, pkg)
			if err != nil {
				pkgw.logger.Error("error filling package status", zap.Error(err))
				return err
			}
			// once we update the package status, an update event
			// will arrive and handle by UpdateFunc later. So we
			// don't need to build the package at this moment.
			return nil
		}
		// Only build pending state packages.
		if pkg.Status.BuildStatus == fv1.BuildStatusPending {
			return pkgw.build(ctx, pkg)
		}
		return nil
	}
	obj, quit := pkgw.pkgQueue.Get()
	if quit {
		return true
	}
	key := obj.(string)
	defer pkgw.pkgQueue.Done(key)
	namespace, name, err := k8sCache.SplitMetaNamespaceKey(key)
	if err != nil {
		pkgw.logger.Error("error splitting key", zap.Error(err))
		pkgw.pkgQueue.Forget(key)
		return false
	}
	pkg, err := pkgw.pkgLister.Packages(namespace).Get(name)
	if k8serrors.IsNotFound(err) {
		pkgw.logger.Info("pkg not found", zap.String("key", key))
		pkgw.pkgQueue.Forget(key)
		return false
	}

	if err != nil {
		if pkgw.pkgQueue.NumRequeues(key) < maxRetries {
			pkgw.pkgQueue.AddRateLimited(key)
			pkgw.logger.Error("error getting env, retrying", zap.Error(err))
		} else {
			pkgw.pkgQueue.Forget(key)
			pkgw.logger.Error("error getting env, retrying, max retries reached", zap.Error(err))
		}
		return false
	}

	ctx := context.Background()
	err = handlePkg(ctx, pkg)
	if err != nil {
		if pkgw.pkgQueue.NumRequeues(key) < maxRetries {
			pkgw.pkgQueue.AddRateLimited(key)
			pkgw.logger.Error("error handling env from envInformer, retrying", zap.String("key", key), zap.Error(err))
		} else {
			pkgw.pkgQueue.Forget(key)
			pkgw.logger.Error("error handling env from envInformer, max retries reached", zap.String("key", key), zap.Error(err))
		}
		return false
	}
	pkgw.pkgQueue.Forget(key)
	return false
}

func (pkgw *packageWatcher) Run(stopCh <-chan struct{}) {
	defer pkgw.pkgQueue.ShutDown()
	if ok := k8sCache.WaitForCacheSync(stopCh, pkgw.pkgListerSynced, pkgw.podListerSynced); !ok {
		pkgw.logger.Fatal("failed to wait for caches to sync")
	}
	for i := 0; i < 4; i++ {
		go wait.Until(pkgw.workerRun("pkgUpdate", pkgw.pkgQueueProcessFunc), time.Second, stopCh)
	}
	<-stopCh
	pkgw.logger.Info("Shutting down worker for packageWatcher")
}

// build helps to update package status, checks environment builder pod status and
// dispatches buildPackage to build source package into deployment package.
// Following is the steps build function takes to complete the whole process.
// 1. Check package status
// 2. Update package status to running state
// 3. Check environment builder pod status
// 4. Call buildPackage to build package
// 5. Update package resource in package ref of functions that share the same package
// 6. Update package status to succeed state
// *. Update package status to failed state,if any one of steps above failed/time out
func (pkgw *packageWatcher) build(ctx context.Context, srcpkg *fv1.Package) error {
	// Ignore duplicate build requests
	key := fmt.Sprintf("%v-%v", srcpkg.ObjectMeta.Name, srcpkg.ObjectMeta.ResourceVersion)
	_, err := pkgw.buildCache.Set(key, srcpkg)
	if err != nil {
		pkgw.logger.Error("error setting package in build cache", zap.Error(err), zap.String("key", key))
		return nil
	}
	defer func() {
		err := pkgw.buildCache.Delete(key)
		if err != nil {
			pkgw.logger.Error("error deleting key from cache", zap.String("key", key), zap.Error(err))
		}
	}()

	pkgw.logger.Info("starting build for package", zap.String("package_name", srcpkg.Name), zap.String("resource_version", srcpkg.ResourceVersion))

	pkg, err := updatePackage(ctx, pkgw.logger, pkgw.fissionClient, srcpkg, fv1.BuildStatusRunning, "", nil)
	if err != nil {
		pkgw.logger.Error("error setting package pending state", zap.Error(err))
		return err
	}

	env, err := pkgw.fissionClient.CoreV1().Environments(pkg.Spec.Environment.Namespace).Get(ctx, pkg.Spec.Environment.Name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		e := "environment does not exist"
		pkgw.logger.Error(e, zap.String("environment", pkg.Spec.Environment.Name))
		_, er := updatePackage(ctx, pkgw.logger, pkgw.fissionClient, pkg,
			fv1.BuildStatusFailed, fmt.Sprintf("%s: %q", e, pkg.Spec.Environment.Name), nil)
		if er != nil {
			pkgw.logger.Error(
				"error updating package",
				zap.String("package_name", pkg.ObjectMeta.Name),
				zap.String("resource_version", pkg.ObjectMeta.ResourceVersion),
				zap.Error(er),
			)
		}
		return nil
	}

	// Create a new BackOff for health check on environment builder pod
	healthCheckBackOff := utils.NewDefaultBackOff()
	//if err != nil {
	//	pkgw.logger.Error("Unable to create BackOff for Health Check", zap.Error(err))
	//}
	// Do health check for environment builder pod
	for healthCheckBackOff.NextExists() {
		// Informer store is not able to use label to find the pod,
		// iterate all available environment builders.
		items, err := pkgw.podLister.List(labels.Everything())
		if err != nil {
			pkgw.logger.Error("error retrieving pod information for environment", zap.Error(err), zap.String("environment", env.ObjectMeta.Name))
			return nil
		}

		if len(items) == 0 {
			pkgw.logger.Info("builder pod does not exist for environment, will retry again later", zap.String("environment", pkg.Spec.Environment.Name))
			time.Sleep(healthCheckBackOff.GetCurrentBackoffDuration())
			continue
		}

		for _, pod := range items {
			// In order to support backward compatibility, for all builder images created in default env,
			// the pods will be created in fission-builder namespace
			builderNs := pkgw.builderNamespace
			if env.ObjectMeta.Namespace != metav1.NamespaceDefault {
				builderNs = env.ObjectMeta.Namespace
			}

			// Filter non-matching pods
			if pod.ObjectMeta.Labels[LABEL_ENV_NAME] != env.ObjectMeta.Name ||
				pod.ObjectMeta.Labels[LABEL_ENV_NAMESPACE] != builderNs ||
				pod.ObjectMeta.Labels[LABEL_ENV_RESOURCEVERSION] != env.ObjectMeta.ResourceVersion {
				continue
			}

			// Pod may become "Running" state but still failed at health check, so use
			// pod.Status.ContainerStatuses instead of pod.Status.Phase to check pod readiness states.
			podIsReady := true

			for _, cStatus := range pod.Status.ContainerStatuses {
				podIsReady = podIsReady && cStatus.Ready
			}

			if !podIsReady {
				pkgw.logger.Info("builder pod is not ready for environment, will retry again later", zap.String("environment", pkg.Spec.Environment.Name))
				time.Sleep(healthCheckBackOff.GetCurrentBackoffDuration())
				break
			}

			// Add the package getter rolebinding to builder sa
			// we continue here if role binding was not setup successfully. this is because without this, the fetcher wont be able to fetch the source pkg into the container and
			// the build will fail eventually
			err := utils.SetupRoleBinding(ctx, pkgw.logger, pkgw.k8sClient, fv1.PackageGetterRB, pkg.ObjectMeta.Namespace, fv1.PackageGetterCR, fv1.ClusterRole, fv1.FissionBuilderSA, builderNs)
			if err != nil {
				pkgw.logger.Error("error setting up role binding for package",
					zap.Error(err),
					zap.String("role_binding", fv1.PackageGetterRB),
					zap.String("package_name", pkg.ObjectMeta.Name),
					zap.String("package_namespace", pkg.ObjectMeta.Namespace))
				continue
			} else {
				pkgw.logger.Info("setup rolebinding for sa package",
					zap.String("sa", fmt.Sprintf("%s.%s", fv1.FissionBuilderSA, builderNs)),
					zap.String("package", fmt.Sprintf("%s.%s", pkg.ObjectMeta.Name, pkg.ObjectMeta.Namespace)))
			}

			uploadResp, buildLogs, err := buildPackage(ctx, pkgw.logger, pkgw.fissionClient, builderNs, pkgw.storageSvcUrl, pkg)
			if err != nil {
				pkgw.logger.Error("error building package", zap.Error(err), zap.String("package_name", pkg.ObjectMeta.Name))
				_, er := updatePackage(ctx, pkgw.logger, pkgw.fissionClient, pkg, fv1.BuildStatusFailed, buildLogs, nil)
				if er != nil {
					pkgw.logger.Error(
						"error updating package",
						zap.String("package_name", pkg.ObjectMeta.Name),
						zap.String("resource_version", pkg.ObjectMeta.ResourceVersion),
						zap.Error(er),
					)
				}
				return nil
			}

			pkgw.logger.Info("starting package info update", zap.String("package_name", pkg.ObjectMeta.Name))

			fnList, err := pkgw.fissionClient.CoreV1().Functions(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
			if err != nil {
				e := "error getting function list"
				pkgw.logger.Error(e, zap.Error(err))
				buildLogs += fmt.Sprintf("%s: %v\n", e, err)
				_, er := updatePackage(ctx, pkgw.logger, pkgw.fissionClient, pkg, fv1.BuildStatusFailed, buildLogs, nil)
				if er != nil {
					pkgw.logger.Error(
						"error updating package",
						zap.String("package_name", pkg.ObjectMeta.Name),
						zap.String("resource_version", pkg.ObjectMeta.ResourceVersion),
						zap.Error(er),
					)
					return nil
				}
				return nil
			}

			// A package may be used by multiple functions. Update
			// functions with old package resource version
			for _, fn := range fnList.Items {
				if fn.Spec.Package.PackageRef.Name == pkg.ObjectMeta.Name &&
					fn.Spec.Package.PackageRef.Namespace == pkg.ObjectMeta.Namespace &&
					fn.Spec.Package.PackageRef.ResourceVersion != pkg.ObjectMeta.ResourceVersion {
					fn.Spec.Package.PackageRef.ResourceVersion = pkg.ObjectMeta.ResourceVersion
					// update CRD
					_, err = pkgw.fissionClient.CoreV1().Functions(fn.ObjectMeta.Namespace).Update(ctx, &fn, metav1.UpdateOptions{})
					if err != nil {
						e := "error updating function package resource version"
						pkgw.logger.Error(e, zap.Error(err))
						buildLogs += fmt.Sprintf("%s: %v\n", e, err)
						_, er := updatePackage(ctx, pkgw.logger, pkgw.fissionClient, pkg, fv1.BuildStatusFailed, buildLogs, nil)
						if er != nil {
							pkgw.logger.Error(
								"error updating package",
								zap.String("package_name", pkg.ObjectMeta.Name),
								zap.String("resource_version", pkg.ObjectMeta.ResourceVersion),
								zap.Error(er),
							)
						}
						return nil
					}
					pkgw.logger.Info("updated function package resource version", zap.Any("function", fn), zap.String("package_name", pkg.ObjectMeta.Name))
				}
			}

			_, err = updatePackage(ctx, pkgw.logger, pkgw.fissionClient, pkg,
				fv1.BuildStatusSucceeded, buildLogs, uploadResp)
			if err != nil {
				pkgw.logger.Error("error updating package info", zap.Error(err), zap.String("package_name", pkg.ObjectMeta.Name))
				_, er := updatePackage(ctx, pkgw.logger, pkgw.fissionClient, pkg, fv1.BuildStatusFailed, buildLogs, nil)
				if er != nil {
					pkgw.logger.Error(
						"error updating package",
						zap.String("package_name", pkg.ObjectMeta.Name),
						zap.String("resource_version", pkg.ObjectMeta.ResourceVersion),
						zap.Error(er),
					)
				}
				return nil
			}

			pkgw.logger.Info("completed package build request", zap.String("package_name", pkg.ObjectMeta.Name))
			return nil
		}
		time.Sleep(healthCheckBackOff.GetNext())
	}
	// build timeout
	_, err = updatePackage(ctx, pkgw.logger, pkgw.fissionClient, pkg,
		fv1.BuildStatusFailed, "Build timeout due to environment builder not ready", nil)
	if err != nil {
		pkgw.logger.Error(
			"error updating package",
			zap.String("package_name", pkg.ObjectMeta.Name),
			zap.String("resource_version", pkg.ObjectMeta.ResourceVersion),
			zap.Error(err),
		)
	}

	pkgw.logger.Error("max retries exceeded in building source package, timeout due to environment builder not ready",
		zap.String("package", fmt.Sprintf("%s.%s", pkg.ObjectMeta.Name, pkg.ObjectMeta.Namespace)))
	return nil
}

// setInitialBuildStatus sets initial build status to a package if it is empty.
// This normally occurs when the user applies package YAML files that have no status field
// through kubectl.
func setInitialBuildStatus(ctx context.Context, fissionClient *crd.FissionClient, pkg *fv1.Package) (*fv1.Package, error) {
	pkg.Status = fv1.PackageStatus{
		LastUpdateTimestamp: metav1.Time{Time: time.Now().UTC()},
	}
	if !pkg.Spec.Deployment.IsEmpty() {
		// if the deployment archive is not empty,
		// we assume it's a deployable package no matter
		// the source archive is empty or not.
		pkg.Status.BuildStatus = fv1.BuildStatusNone
	} else if !pkg.Spec.Source.IsEmpty() {
		pkg.Status.BuildStatus = fv1.BuildStatusPending
	} else {
		// mark package failed since we cannot do anything with it.
		pkg.Status.BuildStatus = fv1.BuildStatusFailed
		pkg.Status.BuildLog = "Both deploy and source archive are empty"
	}

	return fissionClient.CoreV1().Packages(pkg.Namespace).UpdateStatus(ctx, pkg, metav1.UpdateOptions{})
}
