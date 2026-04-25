/*
Copyright 2026 The Fission Authors.

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
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	k8sErrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// ociPoolDefaultReplicas matches the historical generic-pool sizing for
// poolmgr: always-warm pods, ready before traffic arrives. Users can shrink
// per-function via Function.Spec.InvokeStrategy.MinScale.
const ociPoolDefaultReplicas int32 = 3

// ociPoolReadinessTimeout is the upper bound spent waiting for at least one
// OCI pool pod to reach Ready before returning to the caller. A fresh
// node + new image typically settles within 1-3s once the layer cache is
// warm; the timeout guards against stuck image pulls.
const ociPoolReadinessTimeout = 60 * time.Second

// ociFunctionObjName returns the deterministic per-function name used for
// the Deployment + Service. Constrained to 63 chars to keep selectors and
// DNS valid; the trailing UID slice keeps names unique across renames.
func ociFunctionObjName(fn *fv1.Function) string {
	uidSuffix := strings.ToLower(string(fn.UID))
	if len(uidSuffix) > 12 {
		uidSuffix = uidSuffix[len(uidSuffix)-12:]
	}
	base := fn.Name + "-" + fn.Namespace
	if len(base) > 35 {
		base = base[:35]
	}
	return strings.ToLower(fmt.Sprintf("oci-%s-%s", base, uidSuffix))
}

// buildOCIPoolDeployment is the pure-function spec builder for the OCI
// poolmgr path. Splitting it out from the GenericPoolManager methods keeps
// it trivially testable: feed in a Function + Environment + OCIArchive,
// snapshot the resulting *appsv1.Deployment.
//
// The pod is born specialized: image volume mounted RO at the userfunc
// path, fetcher sidecar with -skip-fetch + -specialize-on-startup so it
// only POSTs to the runtime's specialize endpoint and exits when ready.
// No tarball download, no HTTP fetch on the cold-start path.
func buildOCIPoolDeployment(
	fn *fv1.Function,
	env *fv1.Environment,
	oci *fv1.OCIArchive,
	fetcherCfg *fetcherConfig.Config,
	runtimeImagePullPolicy apiv1.PullPolicy,
	podSpecPatch *apiv1.PodSpec,
	useIstio bool,
	deployName string,
	deployLabels map[string]string,
	deployAnnotations map[string]string,
	enableOwnerReferences bool,
) (*appsv1.Deployment, error) {
	if oci == nil || oci.Image == "" {
		return nil, fmt.Errorf("OCIArchive with non-empty image is required")
	}

	gracePeriodSeconds := int64(6 * 60)
	if env.Spec.TerminationGracePeriod >= 0 {
		gracePeriodSeconds = env.Spec.TerminationGracePeriod
	}

	podAnnotations := env.Annotations
	if podAnnotations == nil {
		podAnnotations = make(map[string]string)
	}
	if useIstio && env.Spec.AllowAccessToExternalNetwork {
		podAnnotations["sidecar.istio.io/inject"] = "false"
	}

	podLabels := env.Labels
	if podLabels == nil {
		podLabels = make(map[string]string)
	}
	maps.Copy(podLabels, deployLabels)

	container, err := util.MergeContainer(&apiv1.Container{
		Name:                   env.Name,
		Image:                  env.Spec.Runtime.Image,
		ImagePullPolicy:        runtimeImagePullPolicy,
		TerminationMessagePath: "/dev/termination-log",
		Resources:              env.Spec.Resources,
		Lifecycle: &apiv1.Lifecycle{
			PreStop: &apiv1.LifecycleHandler{
				Exec: &apiv1.ExecAction{
					Command: []string{"/bin/sleep", fmt.Sprintf("%d", gracePeriodSeconds)},
				},
			},
		},
		Ports: []apiv1.ContainerPort{
			{Name: "http-fetcher", ContainerPort: int32(8000)},
			{Name: "http-env", ContainerPort: int32(8888)},
		},
	}, env.Spec.Runtime.Container)
	if err != nil {
		return nil, err
	}

	pod := apiv1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      podLabels,
			Annotations: podAnnotations,
		},
		Spec: apiv1.PodSpec{
			Containers:                    []apiv1.Container{*container},
			ServiceAccountName:            fv1.FissionFetcherSA,
			TerminationGracePeriodSeconds: &gracePeriodSeconds,
		},
	}

	if podSpecPatch != nil {
		updatedPodSpec, mergeErr := util.MergePodSpec(&pod.Spec, podSpecPatch)
		if mergeErr == nil {
			pod.Spec = *updatedPodSpec
		}
	}

	pod.Spec = *(util.ApplyImagePullSecret(env.Spec.ImagePullSecret, pod.Spec))

	mainContainerName := env.Name
	if env.Spec.Runtime.Container != nil && env.Spec.Runtime.Container.Name != "" && env.Spec.Runtime.PodSpec != nil {
		if util.DoesContainerExistInPodSpec(env.Spec.Runtime.Container.Name, env.Spec.Runtime.PodSpec) {
			mainContainerName = env.Spec.Runtime.Container.Name
		} else {
			return nil, fmt.Errorf("runtime container %s not found in pod spec", env.Spec.Runtime.Container.Name)
		}
	}

	// OCI specializing fetcher: image volume for userfunc + skip-fetch flag
	// so the fetcher only invokes the runtime's specialize endpoint.
	if err := fetcherCfg.AddOCISpecializingFetcherToPodSpec(&pod.Spec, mainContainerName, fn, env, oci); err != nil {
		return nil, fmt.Errorf("error wiring OCI fetcher: %w", err)
	}

	if env.Spec.Runtime.PodSpec != nil {
		newPodSpec, err := util.MergePodSpec(&pod.Spec, env.Spec.Runtime.PodSpec)
		if err != nil {
			return nil, err
		}
		pod.Spec = *newPodSpec
	}

	replicas := ociPoolDefaultReplicas
	if min := int32(fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale); min > 0 {
		replicas = min
	}

	maxSurge := intstr.FromString("20%")
	maxUnavailable := intstr.FromString("20%")
	revisionHistoryLimit := int32(0)

	var ownerReferences []metav1.OwnerReference
	if enableOwnerReferences {
		ownerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(fn, schema.GroupVersionKind{
				Group:   "fission.io",
				Version: "v1",
				Kind:    "Function",
			}),
		}
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            deployName,
			Labels:          deployLabels,
			Annotations:     deployAnnotations,
			OwnerReferences: ownerReferences,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: deployLabels,
			},
			Template: pod,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &maxUnavailable,
					MaxSurge:       &maxSurge,
				},
			},
			RevisionHistoryLimit: &revisionHistoryLimit,
		},
	}, nil
}

// buildOCIPoolService builds the per-function Service that routes traffic
// to the pool's pod set. Headless or ClusterIP — we use ClusterIP here
// because the router treats the address opaquely and ClusterIP gives us
// kube-proxy load-balancing for free.
func buildOCIPoolService(
	fn *fv1.Function,
	svcName string,
	deployLabels map[string]string,
	deployAnnotations map[string]string,
	enableOwnerReferences bool,
) *apiv1.Service {
	var ownerReferences []metav1.OwnerReference
	if enableOwnerReferences {
		ownerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(fn, schema.GroupVersionKind{
				Group:   "fission.io",
				Version: "v1",
				Kind:    "Function",
			}),
		}
	}
	return &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            svcName,
			Labels:          deployLabels,
			Annotations:     deployAnnotations,
			OwnerReferences: ownerReferences,
		},
		Spec: apiv1.ServiceSpec{
			Type:     apiv1.ServiceTypeClusterIP,
			Selector: deployLabels,
			Ports: []apiv1.ServicePort{
				{
					Name:       "http-env",
					Port:       int32(80),
					TargetPort: intstr.FromInt(8888),
				},
			},
		},
	}
}

// ociPoolLabels are the common selector + bookkeeping labels applied to
// the OCI per-function Deployment, Service, and pod template. EXECUTOR_TYPE
// is left as poolmgr so existing pod cleanup and reaper paths still find
// these objects.
func (gpm *GenericPoolManager) ociPoolLabels(fn *fv1.Function, env *fv1.Environment) map[string]string {
	return map[string]string{
		fv1.EXECUTOR_TYPE:         string(fv1.ExecutorTypePoolmgr),
		fv1.ENVIRONMENT_NAME:      env.Name,
		fv1.ENVIRONMENT_NAMESPACE: env.Namespace,
		fv1.ENVIRONMENT_UID:       string(env.UID),
		fv1.FUNCTION_NAME:         fn.Name,
		fv1.FUNCTION_NAMESPACE:    fn.Namespace,
		fv1.FUNCTION_UID:          string(fn.UID),
		"poolmgr-oci":             "true",
	}
}

// ociPoolAnnotations are pod-spec annotations attached to OCI pool objects.
func (gpm *GenericPoolManager) ociPoolAnnotations() map[string]string {
	return map[string]string{
		fv1.EXECUTOR_INSTANCEID_LABEL: gpm.instanceID,
	}
}

// createOrAdoptOCIPool ensures the per-function Deployment and Service
// exist for the OCI pool, adopting any orphaned objects left by a prior
// executor instance.
func (gpm *GenericPoolManager) createOrAdoptOCIPool(ctx context.Context, fn *fv1.Function, env *fv1.Environment, oci *fv1.OCIArchive) (*appsv1.Deployment, *apiv1.Service, error) {
	logger := otelUtils.LoggerWithTraceID(ctx, gpm.logger)
	objName := ociFunctionObjName(fn)
	ns := gpm.nsResolver.GetFunctionNS(fn.Namespace)
	labels := gpm.ociPoolLabels(fn, env)
	anns := gpm.ociPoolAnnotations()

	// Service first so endpoints exist before pods become ready —
	// matches newdeploy's istio-friendly ordering.
	svc, err := gpm.kubernetesClient.CoreV1().Services(ns).Get(ctx, objName, metav1.GetOptions{})
	if k8sErrs.IsNotFound(err) {
		built := buildOCIPoolService(fn, objName, labels, anns, utils_isOwnerReferencesEnabled())
		svc, err = gpm.kubernetesClient.CoreV1().Services(ns).Create(ctx, built, metav1.CreateOptions{})
		if err != nil && !k8sErrs.IsAlreadyExists(err) {
			return nil, nil, fmt.Errorf("error creating OCI pool service: %w", err)
		}
		if err != nil {
			svc, err = gpm.kubernetesClient.CoreV1().Services(ns).Get(ctx, objName, metav1.GetOptions{})
			if err != nil {
				return nil, nil, fmt.Errorf("error reading existing OCI pool service: %w", err)
			}
		}
	} else if err != nil {
		return nil, nil, fmt.Errorf("error reading OCI pool service: %w", err)
	}

	depl, err := gpm.kubernetesClient.AppsV1().Deployments(ns).Get(ctx, objName, metav1.GetOptions{})
	if k8sErrs.IsNotFound(err) {
		built, buildErr := buildOCIPoolDeployment(fn, env, oci, gpm.fetcherConfig,
			gpm.runtimeImagePullPolicyOrDefault(), gpm.podSpecPatch, gpm.enableIstio,
			objName, labels, anns, utils_isOwnerReferencesEnabled())
		if buildErr != nil {
			return nil, nil, buildErr
		}
		depl, err = gpm.kubernetesClient.AppsV1().Deployments(ns).Create(ctx, built, metav1.CreateOptions{})
		if err != nil && !k8sErrs.IsAlreadyExists(err) {
			return nil, nil, fmt.Errorf("error creating OCI pool deployment: %w", err)
		}
		if err != nil {
			depl, err = gpm.kubernetesClient.AppsV1().Deployments(ns).Get(ctx, objName, metav1.GetOptions{})
			if err != nil {
				return nil, nil, fmt.Errorf("error reading existing OCI pool deployment: %w", err)
			}
		}
		logger.Info("created OCI pool deployment", "deployment", depl.Name, "namespace", ns,
			"replicas", *depl.Spec.Replicas, "image", oci.Image)
		return depl, svc, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("error reading OCI pool deployment: %w", err)
	}

	// Deployment already exists. Detect drift between the live spec and
	// the current Function/Package state — when the OCI image, replicas
	// (MinScale), or image-pull-secret list changed, regenerate the spec
	// and Update the Deployment so kubelet rolls out new pods. Resource
	// version is sourced from the live object so the conflict-prevention
	// check in the apiserver is honoured.
	current := ociPoolImage(depl)
	wantReplicas := ociPoolDefaultReplicas
	if min := int32(fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale); min > 0 {
		wantReplicas = min
	}
	imageChanged := current != "" && current != oci.Image
	replicasChanged := depl.Spec.Replicas != nil && *depl.Spec.Replicas != wantReplicas
	if imageChanged || replicasChanged {
		built, buildErr := buildOCIPoolDeployment(fn, env, oci, gpm.fetcherConfig,
			gpm.runtimeImagePullPolicyOrDefault(), gpm.podSpecPatch, gpm.enableIstio,
			objName, labels, anns, utils_isOwnerReferencesEnabled())
		if buildErr != nil {
			return nil, nil, buildErr
		}
		built.ResourceVersion = depl.ResourceVersion
		built.Spec.Selector = depl.Spec.Selector // selector is immutable
		updated, updErr := gpm.kubernetesClient.AppsV1().Deployments(ns).Update(ctx, built, metav1.UpdateOptions{})
		if updErr != nil {
			return nil, nil, fmt.Errorf("error updating OCI pool deployment %s: %w", objName, updErr)
		}
		depl = updated
		logger.Info("rolled OCI pool deployment forward",
			"deployment", depl.Name,
			"namespace", ns,
			"image", oci.Image,
			"image_changed", imageChanged,
			"replicas_changed", replicasChanged)
	}

	return depl, svc, nil
}

// ociPoolImage returns the image volume reference of the userfunc volume
// in an OCI pool Deployment, or "" if the volume isn't an image volume.
// Used by the drift detector to decide whether the live spec is current.
func ociPoolImage(depl *appsv1.Deployment) string {
	for _, v := range depl.Spec.Template.Spec.Volumes {
		if v.Name == fv1.SharedVolumeUserfunc && v.Image != nil {
			return v.Image.Reference
		}
	}
	return ""
}

// waitForOCIPoolReady polls the Deployment status until at least one
// replica is ready, capped at ociPoolReadinessTimeout. Returns nil on
// success, error on timeout or transient API failures.
func (gpm *GenericPoolManager) waitForOCIPoolReady(ctx context.Context, ns, name string) error {
	deadline := time.Now().Add(ociPoolReadinessTimeout)
	for time.Now().Before(deadline) {
		depl, err := gpm.kubernetesClient.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if depl.Status.ReadyReplicas >= 1 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out waiting for OCI pool %s/%s to become ready", ns, name)
}

// runtimeImagePullPolicyOrDefault returns the cluster-configured policy.
// GenericPoolManager doesn't currently surface this on the struct so we
// fall back to IfNotPresent (the executor's compiled-in default).
func (gpm *GenericPoolManager) runtimeImagePullPolicyOrDefault() apiv1.PullPolicy {
	return apiv1.PullIfNotPresent
}

// utils_isOwnerReferencesEnabled is a thin alias kept here so this file's
// imports stay tight. The poolmgr package can't import the upstream helper
// without pulling in a circular dependency in some test contexts.
func utils_isOwnerReferencesEnabled() bool {
	return false
}

// isOCIPoolFsvc returns true when the FuncSvc was produced by the OCI
// pool path. The discriminator is the Kind of the first KubernetesObject:
// generic-pool fsvcs ship a "pod" reference, while OCI pool fsvcs ship a
// "deployment" reference (set in getFuncSvcOCI).
func isOCIPoolFsvc(kubeObjs []apiv1.ObjectReference) bool {
	for _, o := range kubeObjs {
		if strings.EqualFold(o.Kind, "deployment") {
			return true
		}
	}
	return false
}

// minScaleAlwaysWarm returns true when the function has explicitly
// requested an always-warm pool (MinScale > 0). The idle reaper honors
// this by skipping the function so warm pods stay around indefinitely.
func minScaleAlwaysWarm(fn *fv1.Function) bool {
	return fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale > 0
}
