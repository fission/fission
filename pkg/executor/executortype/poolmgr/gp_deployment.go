// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package poolmgr

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	k8sErrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/utils"

	"github.com/fission/fission/pkg/svcinfo"
)

// getPoolName returns a unique name for a pool's deployment: per environment,
// plus a short image-hash suffix for per-image pools (RFC-0001 Path B).
func getPoolName(env *fv1.Environment, imageHash string) string {
	// TODO: get rid of resource version here
	var envPodName string

	// To fit the 63 character limit; per-image pools spend 9 characters on
	// the "-<hash[:8]>" suffix, so their env segments get a tighter budget.
	segmentBudget, capPerSegment := 37, 18
	suffix := ""
	if imageHash != "" {
		segmentBudget, capPerSegment = 28, 13
		suffix = "-" + imageHash[:8]
	}
	if len(env.Name)+len(env.Namespace) < segmentBudget {
		envPodName = env.Name + "-" + env.Namespace
	} else {
		nameLength := min(len(env.Name), capPerSegment)
		namespaceLength := min(len(env.Namespace), capPerSegment)
		envPodName = env.Name[:nameLength] + "-" + env.Namespace[:namespaceLength]
	}

	return "poolmgr-" + strings.ToLower(fmt.Sprintf("%s%s-%s", envPodName, suffix, env.ResourceVersion))
}

func (gp *GenericPool) genDeploymentMeta(env *fv1.Environment) metav1.ObjectMeta {
	deployLabels := gp.getEnvironmentPoolLabels(env)
	deployAnnotations := gp.getDeployAnnotations(env)

	var ownerReferences []metav1.OwnerReference
	if gp.enableOwnerReferences {
		ownerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(env, fv1.SchemeGroupVersion.WithKind("Environment")),
		}
	}
	return metav1.ObjectMeta{
		Name:            getPoolName(env, gp.ociImageHash),
		Labels:          deployLabels,
		Annotations:     deployAnnotations,
		OwnerReferences: ownerReferences,
	}
}

func (gp *GenericPool) genDeploymentSpec(env *fv1.Environment) (*appsv1.DeploymentSpec, error) {
	deployLabels := gp.getEnvironmentPoolLabels(env)
	// Use long terminationGracePeriodSeconds for connection draining in case that
	// pod still runs user functions.
	gracePeriodSeconds := int64(6 * 60)
	if env.Spec.TerminationGracePeriod >= 0 {
		gracePeriodSeconds = env.Spec.TerminationGracePeriod
	}

	podAnnotations := env.Annotations
	if podAnnotations == nil {
		podAnnotations = make(map[string]string)
	}

	// Here, we don't append executor instance-id to pod annotations
	// to prevent unwanted rolling updates occur. Pool manager will
	// append executor instance-id to pod annotations when a pod is chosen
	// for function specialization.

	// Stamp the HMAC key-scheme so the executor signs this pod's /specialize
	// calls with the key its fetcher will actually verify with (version-aware
	// signing). Stable per-namespace under dynamic tenancy — present for tenant
	// namespaces, absent otherwise — so it adds at most one rollout when tenancy
	// is first enabled, never ongoing churn. See fetcherSigningNamespace.
	if shouldStampNamespaceKeyScheme(gp.fnNamespace, utils.DefaultNSResolver()) {
		podAnnotations[fv1.AuthKeySchemeAnnotation] = fv1.AuthKeySchemeNamespace
	}

	if gp.useIstio && env.Spec.AllowAccessToExternalNetwork {
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
		ImagePullPolicy:        gp.runtimeImagePullPolicy,
		TerminationMessagePath: "/dev/termination-log",
		Resources:              env.Spec.Resources,
		// Connection-draining preStop hook; see utils.DrainLifecycle.
		Lifecycle: utils.DrainLifecycle(gracePeriodSeconds),
		// https://istio.io/docs/setup/kubernetes/additional-setup/requirements/
		Ports: []apiv1.ContainerPort{
			{
				Name:          "http-fetcher",
				ContainerPort: int32(svcinfo.PortFetcher),
			},
			{
				Name:          "http-env",
				ContainerPort: int32(svcinfo.PortEnvRuntime),
			},
		},
	}, env.Spec.Runtime.Container)
	if err != nil {
		return nil, err
	}

	// AutomountServiceAccountToken=false stops Kubernetes from injecting
	// the fission-fetcher ServiceAccount token into every container in
	// the pod. The fetcher container re-mounts the token via the
	// projected volume defined below — the user-code container does not.
	// See GHSA-85g2-pmrx-r49q.
	automountSAToken := false
	// Fetcherless Path B pods (B-direct) have no fetcher container, so they
	// don't carry the fetcher SA token projected volume either; plain pools
	// and the fetcher-retained variant (B-fetcher, RFC-0012) do.
	var baseVolumes []apiv1.Volume
	if gp.hasFetcher() {
		baseVolumes = append(baseVolumes, util.FetcherSATokenProjectedVolume())
	}
	pod := apiv1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      podLabels,
			Annotations: podAnnotations,
		},
		Spec: apiv1.PodSpec{
			Containers:                   []apiv1.Container{*container},
			ServiceAccountName:           fv1.FissionFetcherSA,
			AutomountServiceAccountToken: &automountSAToken,
			// TerminationGracePeriodSeconds should be equal to the
			// preStop sleep so SIGTERM is only sent once the drain
			// window has elapsed.
			TerminationGracePeriodSeconds: &gracePeriodSeconds,
			Volumes:                       baseVolumes,
		},
	}

	if gp.podSpecPatch != nil {
		updatedPodSpec, err := util.MergePodSpec(&pod.Spec, gp.podSpecPatch)
		if err == nil {
			pod.Spec = *updatedPodSpec
		} else {
			gp.logger.Error(err, "Failed to merge the specs")
		}
		// Re-clamp after the merge: MergePodSpec propagates a non-nil
		// AutomountServiceAccountToken from the patch, which would otherwise
		// re-enable the kubelet auto-mount on the user container. See
		// GHSA-85g2-pmrx-r49q.
		pod.Spec.AutomountServiceAccountToken = new(false)
	}

	pod.Spec = *(util.ApplyImagePullSecret(env.Spec.ImagePullSecret, pod.Spec))

	poolsize := getEnvPoolSize(env)
	switch env.Spec.AllowedFunctionsPerContainer {
	case fv1.AllowedFunctionsPerContainerInfinite:
		poolsize = 1
	}
	if gp.oci != nil {
		// Per-image pools keep ONE warm pod (RFC-0012): the env's poolsize
		// multiplies per PACKAGE here (N built packages x poolsize pods,
		// for both variants), and the kubelet's image cache makes recreation
		// cheap — warm depth is the generic pool's job, bounded-economics is
		// this pool's. This is the RFC's Gate C poolsize lever, applied by
		// default.
		poolsize = 1
	}

	deploymentSpec := appsv1.DeploymentSpec{
		// TODO: fix this hardcoded value
		Replicas: &poolsize,
		Selector: &metav1.LabelSelector{
			MatchLabels: deployLabels,
		},
		Template: pod,
	}

	// If custom runtime container name - default env name
	mainContainerName := env.Name
	if env.Spec.Runtime.Container != nil && env.Spec.Runtime.Container.Name != "" && env.Spec.Runtime.PodSpec != nil {
		if util.DoesContainerExistInPodSpec(env.Spec.Runtime.Container.Name, env.Spec.Runtime.PodSpec) {
			mainContainerName = env.Spec.Runtime.Container.Name
		} else {
			return nil, fmt.Errorf("runtime container %s not found in pod spec", env.Spec.Runtime.Container.Name)
		}
	}

	// Order of merging is important here - first fetcher, then containers and lastly pod spec.
	// Fetcherless Path B pods (B-direct) skip the fetcher entirely: the
	// kubelet mounts the code; specialization is load-only (see
	// loadOnlySpecialize). B-fetcher pods keep it for Secrets/ConfigMaps.
	if gp.hasFetcher() {
		err = gp.fetcherConfig.AddFetcherToPodSpec(&deploymentSpec.Template.Spec, mainContainerName, gp.fnNamespace)
		if err != nil {
			return nil, err
		}
	}

	if env.Spec.Runtime.PodSpec != nil {
		newPodSpec, err := util.MergePodSpec(&deploymentSpec.Template.Spec, env.Spec.Runtime.PodSpec)
		if err != nil {
			return nil, err
		}
		deploymentSpec.Template.Spec = *newPodSpec
		// Re-clamp after the merge: MergePodSpec propagates a non-nil
		// AutomountServiceAccountToken from env.Spec.Runtime.PodSpec, which
		// would otherwise re-enable the kubelet auto-mount on the user
		// container. See GHSA-85g2-pmrx-r49q.
		deploymentSpec.Template.Spec.AutomountServiceAccountToken = new(false)
	}

	if gp.oci != nil {
		// Path B: mount the package image read-only at the fetcher's store
		// path — the path the load request names (LoadReq.FilePath =
		// <sharedMountPath>/deployarchive). Eligibility guarantees a v2+,
		// non-infinite, non-KeepArchive env, so the store name is the fixed
		// deployarchive constant. B-direct mounts on the runtime container
		// only; B-fetcher also mounts on the fetcher container, whose
		// exists-early-exit then skips the pull (newdeploy's shipped
		// pattern). Applied AFTER every MergePodSpec (same convention as the
		// SA-token re-clamps) so a runtime pod spec cannot strip or shadow
		// the code mount.
		containers := []string{mainContainerName}
		if gp.ociFetcherVariant {
			containers = append(containers, util.FetcherContainerName)
		}
		if err := util.AddImageVolume(&deploymentSpec.Template.Spec, gp.oci,
			filepath.Join(gp.fetcherConfig.SharedMountPath(), fetcherConfig.TargetFilenameDeployArchive),
			containers...); err != nil {
			return nil, err
		}
		if !gp.ociFetcherVariant {
			// B-direct: no fetcher container, so no SA token to re-mount.
			return &deploymentSpec, nil
		}
	}

	// Re-mount the fission-fetcher SA token at the canonical Kubernetes
	// path on the fetcher container only. The pod-level
	// AutomountServiceAccountToken=false flag set above suppresses the
	// implicit mount on every container, including fetcher, so we have to
	// add it back explicitly here. This must run AFTER the env.Spec.Runtime.PodSpec
	// merge — MergePodSpec can append additional volumeMounts to the fetcher
	// container, including one at this same path, and kubelet would reject
	// the pod with a duplicate-mount-path error. The helper strips any
	// pre-existing mount at the path before adding its own, so running it
	// last guarantees a single mount on the fetcher container backed by the
	// projected SA token volume. See GHSA-85g2-pmrx-r49q.
	if err := util.MountFetcherSATokenOnFetcher(&deploymentSpec.Template.Spec); err != nil {
		return nil, err
	}
	return &deploymentSpec, nil
}

// A pool is a deployment of generic containers for an env.  This
// creates the pool but doesn't wait for any pods to be ready.
func (gp *GenericPool) createPoolDeployment(ctx context.Context, env *fv1.Environment) error {
	// avoid create/update/delete pool deployment at the same time
	gp.lock.Lock()
	defer gp.lock.Unlock()

	deploymentMeta := gp.genDeploymentMeta(env)
	deploymentSpec, err := gp.genDeploymentSpec(env)
	if err != nil {
		return err
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: deploymentMeta,
		Spec:       *deploymentSpec,
	}
	depl, err := gp.kubernetesClient.AppsV1().Deployments(gp.fnNamespace).Get(ctx, deployment.Name, metav1.GetOptions{})
	if err == nil {
		if depl.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] != gp.instanceID {
			deployment.Annotations[fv1.EXECUTOR_INSTANCEID_LABEL] = gp.instanceID
			// Update with the latest deployment spec. Kubernetes will trigger
			// rolling update if spec is different from the one in the cluster.
			depl, err = gp.kubernetesClient.AppsV1().Deployments(gp.fnNamespace).Update(ctx, deployment, metav1.UpdateOptions{})
		}
		gp.deployment = depl
		return err
	} else if !k8sErrs.IsNotFound(err) {
		gp.logger.Error(err, "error getting deployment in kubernetes", "deployment", deployment.Name)
		return err
	}

	depl, err = gp.kubernetesClient.AppsV1().Deployments(gp.fnNamespace).Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil {
		gp.logger.Error(err, "error creating deployment in kubernetes", "deployment", deployment.Name)
		return err
	}

	gp.deployment = depl
	gp.logger.Info("deployment created", "deployment", depl.Name, "ns", depl.Namespace, "environment", env)

	return nil
}

func (gp *GenericPool) updatePoolDeployment(ctx context.Context, env *fv1.Environment) error {
	// avoid create/update/delete pool deployment at the same time
	gp.lock.Lock()
	defer gp.lock.Unlock()
	logger := gp.logger.WithValues("env", env.Name, "namespace", env.Namespace)
	if gp.env.ResourceVersion == env.ResourceVersion {
		logger.V(1).Info("env resource version matching with pool env")
		return nil
	}
	newDeployment := gp.deployment.DeepCopy()
	spec, err := gp.genDeploymentSpec(env)
	if err != nil {
		logger.Error(err, "error generating deployment spec")
		return err
	}
	newDeployment.Spec = *spec
	deployMeta := gp.genDeploymentMeta(env)
	deployMeta.Name = gp.deployment.Name
	newDeployment.ObjectMeta = deployMeta

	poolsize := getEnvPoolSize(env)
	switch env.Spec.AllowedFunctionsPerContainer {
	case fv1.AllowedFunctionsPerContainerInfinite:
		poolsize = 1
	}
	if gp.oci != nil {
		// Per-image pools keep ONE warm pod (RFC-0012): the env's poolsize
		// multiplies per PACKAGE here (N built packages x poolsize pods,
		// for both variants), and the kubelet's image cache makes recreation
		// cheap — warm depth is the generic pool's job, bounded-economics is
		// this pool's. This is the RFC's Gate C poolsize lever, applied by
		// default.
		poolsize = 1
	}
	newDeployment.Spec.Replicas = &poolsize

	depl, err := gp.kubernetesClient.AppsV1().Deployments(gp.fnNamespace).Update(ctx, newDeployment, metav1.UpdateOptions{})
	if err != nil {
		logger.Error(err, "error updating deployment in kubernetes", "deployment", depl.Name)
		return err
	}
	// possible concurrency issue here as
	// gp.env and gp.deployment referenced at few places
	// we can move update pool to gpm.service if required
	gp.env = env
	gp.deployment = depl
	logger.Info("Updated deployment for pool", "deployment", depl.Name)
	return nil
}
