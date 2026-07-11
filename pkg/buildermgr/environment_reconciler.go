// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/utils"

	"github.com/fission/fission/pkg/svcinfo"
)

const (
	LABEL_ENV_NAME            = "envName"
	LABEL_ENV_NAMESPACE       = "envNamespace"
	LABEL_ENV_RESOURCEVERSION = "envResourceVersion"
	LABEL_DEPLOYMENT_OWNER    = "owner"
	BUILDER_MGR               = "buildermgr"
)

var (
	deletePropagation = metav1.DeletePropagationBackground
	delOpt            = metav1.DeleteOptions{PropagationPolicy: &deletePropagation}
)

// EnvironmentReconciler keeps a per-Environment builder Service + Deployment in
// sync with the Environment CR. It replaces the informer-driven
// environmentWatcher: controller-runtime's workqueue owns scheduling, the
// GenerationChangedPredicate (in controller.Register) drops status-only updates,
// and reconciliation is level-based — the desired builder objects are derived
// from the live Environment on every call rather than from an in-memory cache,
// so the controller self-heals if the cache and the cluster drift.
//
// The builder Service/Deployment name embeds env.ResourceVersion
// ("<env.Name>-<env.ResourceVersion>"); a spec change yields a new name, so each
// reconcile first deletes builder objects orphaned by a previous generation,
// then ensures the current-generation pair exists.
//
// This reconciler is intentionally STATUS-SILENT — see the note in reconcile()
// for why writing EnvironmentConditionReady here would break source-archive
// builds.
type EnvironmentReconciler struct {
	logger                 logr.Logger
	client                 client.Client
	kubernetesClient       kubernetes.Interface
	nsResolver             *utils.NamespaceResolver
	fetcherConfig          *fetcherConfig.Config
	builderImagePullPolicy apiv1.PullPolicy
	useIstio               bool
	podSpecPatch           *apiv1.PodSpec
	enableOwnerReferences  bool
}

func makeEnvironmentReconciler(
	logger logr.Logger,
	client client.Client,
	kubernetesClient kubernetes.Interface,
	fetcherConfig *fetcherConfig.Config,
	podSpecPatch *apiv1.PodSpec) *EnvironmentReconciler {

	useIstio := false
	enableIstio := os.Getenv("ENABLE_ISTIO")
	if len(enableIstio) > 0 {
		istio, err := strconv.ParseBool(enableIstio)
		if err != nil {
			logger.Error(err, "Failed to parse ENABLE_ISTIO, defaults to false")
		}
		useIstio = istio
	}

	return &EnvironmentReconciler{
		logger:                 logger.WithName("environment_reconciler"),
		client:                 client,
		kubernetesClient:       kubernetesClient,
		nsResolver:             utils.DefaultNSResolver(),
		builderImagePullPolicy: utils.GetImagePullPolicy(os.Getenv("BUILDER_IMAGE_PULL_POLICY")),
		useIstio:               useIstio,
		fetcherConfig:          fetcherConfig,
		podSpecPatch:           podSpecPatch,
		enableOwnerReferences:  utils.IsOwnerReferencesEnabled(),
	}
}

func (r *EnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	env := &fv1.Environment{}
	if err := r.client.Get(ctx, req.NamespacedName, env); err != nil {
		if apierrors.IsNotFound(err) {
			// Environment deleted: tear down its builder Service + Deployment.
			// With owner references enabled Kubernetes garbage-collects them
			// too; this explicit delete is the primary path when they are
			// disabled (the default) and a harmless no-op once GC has run.
			r.deleteBuilder(ctx, req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// builder is not supported with the v1 interface; ignore envs without a
	// builder image.
	if env.Spec.Version == 1 || len(env.Spec.Builder.Image) == 0 {
		return ctrl.Result{}, nil
	}

	ns := r.nsResolver.GetBuilderNS(env.Namespace)
	if err := r.deleteStaleBuilders(ctx, env, ns); err != nil {
		r.logger.Error(err, "error removing stale builder objects", "env_name", env.Name, "builder_namespace", ns)
		return ctrl.Result{}, err
	}
	if err := r.ensureBuilder(ctx, env, ns); err != nil {
		r.logger.Error(err, "error ensuring builder service", "env_name", env.Name, "builder_namespace", ns)
		return ctrl.Result{}, err
	}

	// NOTE: writing EnvironmentConditionReady here would bump env.RV via the
	// status subresource, but the builder service name (and its DNS lookup in
	// pkg/buildermgr/common.go.buildPackage) is "<env.Name>-<env.ResourceVersion>".
	// A status-driven RV bump therefore renames the *expected* service without
	// renaming the *actual* one, breaking every subsequent source-archive build.
	// Decoupling the builder service name from env.ResourceVersion is follow-up
	// work; until then this controller does not write EnvironmentConditionReady.
	return ctrl.Result{}, nil
}

func (r *EnvironmentReconciler) getDeploymentLabels(envName string) map[string]string {
	return map[string]string{
		LABEL_DEPLOYMENT_OWNER: BUILDER_MGR,
		LABEL_ENV_NAME:         envName,
	}
}

func (r *EnvironmentReconciler) getLabels(envName string, envNamespace string, envResourceVersion string) map[string]string {
	return map[string]string{
		LABEL_ENV_NAME:            envName,
		LABEL_ENV_NAMESPACE:       envNamespace,
		LABEL_ENV_RESOURCEVERSION: envResourceVersion,
		LABEL_DEPLOYMENT_OWNER:    BUILDER_MGR,
	}
}

// ensureBuilder creates the builder Service + Deployment for the current
// Environment generation if they do not already exist. createBuilderService /
// createBuilderDeployment are skipped when a matching (current-RV) object is
// already present, so this is idempotent across repeated reconciles.
func (r *EnvironmentReconciler) ensureBuilder(ctx context.Context, env *fv1.Environment, ns string) error {
	sel := r.getLabels(env.Name, ns, env.ResourceVersion)

	svcList, err := r.getBuilderServiceList(ctx, sel, ns)
	if err != nil {
		return err
	}
	switch len(svcList) {
	case 0:
		if _, err := r.createBuilderService(ctx, env, ns); err != nil {
			return fmt.Errorf("error creating builder service for environment %s in namespace %s: %w", env.Name, ns, err)
		}
	case 1:
		// already present
	default:
		return fmt.Errorf("found more than one builder service for environment %s in namespace %s", env.Name, ns)
	}

	deployList, err := r.getBuilderDeploymentList(ctx, sel, ns)
	if err != nil {
		return err
	}
	switch len(deployList) {
	case 0:
		if _, err := r.createBuilderDeployment(ctx, env, ns); err != nil {
			return fmt.Errorf("error creating builder deployment for environment %s in namespace %s: %w", env.Name, ns, err)
		}
	case 1:
		// already present
	default:
		return fmt.Errorf("found more than one builder deployment for environment %s in namespace %s", env.Name, ns)
	}
	return nil
}

// deleteStaleBuilders removes builder Services/Deployments owned by this
// Environment whose embedded ResourceVersion label no longer matches the live
// Environment — i.e. objects left behind by a previous generation.
func (r *EnvironmentReconciler) deleteStaleBuilders(ctx context.Context, env *fv1.Environment, ns string) error {
	sel := r.getDeploymentLabels(env.Name)

	svcList, err := r.getBuilderServiceList(ctx, sel, ns)
	if err != nil {
		return err
	}
	for i := range svcList {
		svc := &svcList[i]
		if svc.Labels[LABEL_ENV_RESOURCEVERSION] == env.ResourceVersion {
			continue
		}
		if err := r.deleteBuilderServiceByName(ctx, svc.Name, svc.Namespace); err != nil {
			return err
		}
		r.logger.Info("deleted stale builder service", "service_name", svc.Name, "service_namespace", svc.Namespace)
	}

	deployList, err := r.getBuilderDeploymentList(ctx, sel, ns)
	if err != nil {
		return err
	}
	for i := range deployList {
		deploy := &deployList[i]
		if deploy.Labels[LABEL_ENV_RESOURCEVERSION] == env.ResourceVersion {
			continue
		}
		if err := r.deleteBuilderDeploymentByName(ctx, deploy.Name, deploy.Namespace); err != nil {
			return err
		}
		r.logger.Info("deleted stale builder deployment", "deployment_name", deploy.Name, "deployment_namespace", deploy.Namespace)
	}
	return nil
}

// deleteBuilder removes every builder Service + Deployment owned by the named
// Environment (across generations). Used on Environment deletion. Best-effort:
// individual delete failures are logged and the rest still proceed.
func (r *EnvironmentReconciler) deleteBuilder(ctx context.Context, envNamespace, envName string) {
	ns := r.nsResolver.GetBuilderNS(envNamespace)
	sel := r.getDeploymentLabels(envName)

	svcList, err := r.getBuilderServiceList(ctx, sel, ns)
	if err != nil {
		r.logger.Error(err, "error listing builder services for deletion", "env_name", envName, "builder_namespace", ns)
	}
	for i := range svcList {
		svc := &svcList[i]
		if err := r.deleteBuilderServiceByName(ctx, svc.Name, svc.Namespace); err != nil {
			r.logger.Error(err, "error removing builder service", "service_name", svc.Name, "service_namespace", svc.Namespace, "env_name", envName)
		}
	}

	deployList, err := r.getBuilderDeploymentList(ctx, sel, ns)
	if err != nil {
		r.logger.Error(err, "error listing builder deployments for deletion", "env_name", envName, "builder_namespace", ns)
	}
	for i := range deployList {
		deploy := &deployList[i]
		if err := r.deleteBuilderDeploymentByName(ctx, deploy.Name, deploy.Namespace); err != nil {
			r.logger.Error(err, "error removing builder deployment", "deployment_name", deploy.Name, "deployment_namespace", deploy.Namespace, "env_name", envName)
		}
	}
	if len(svcList) > 0 || len(deployList) > 0 {
		r.logger.Info("builder service deleted", "env_name", envName, "namespace", ns)
	}
}

func (r *EnvironmentReconciler) deleteBuilderServiceByName(ctx context.Context, name, namespace string) error {
	err := r.kubernetesClient.CoreV1().
		Services(namespace).
		Delete(ctx, name, delOpt)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("error deleting builder service %s.%s: %w", name, namespace, err)
	}
	return nil
}

func (r *EnvironmentReconciler) deleteBuilderDeploymentByName(ctx context.Context, name, namespace string) error {
	err := r.kubernetesClient.AppsV1().
		Deployments(namespace).
		Delete(ctx, name, delOpt)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("error deleting builder deployment %s.%s: %w", name, namespace, err)
	}
	return nil
}

func (r *EnvironmentReconciler) getBuilderServiceList(ctx context.Context, sel map[string]string, ns string) ([]apiv1.Service, error) {
	svcList, err := r.kubernetesClient.CoreV1().Services(ns).List(
		ctx,
		metav1.ListOptions{
			LabelSelector: labels.Set(sel).AsSelector().String(),
		})
	if err != nil {
		return nil, fmt.Errorf("error getting builder service list for namespace %s: %w", ns, err)
	}
	return svcList.Items, nil
}

func (r *EnvironmentReconciler) createBuilderService(ctx context.Context, env *fv1.Environment, ns string) (*apiv1.Service, error) {
	name := fmt.Sprintf("%v-%v", env.Name, env.ResourceVersion)
	sel := r.getLabels(env.Name, ns, env.ResourceVersion)
	var ownerReferences []metav1.OwnerReference
	if r.enableOwnerReferences {
		ownerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(env, fv1.SchemeGroupVersion.WithKind("Environment")),
		}
	}
	service := apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       ns,
			Name:            name,
			Labels:          sel,
			OwnerReferences: ownerReferences,
		},
		Spec: apiv1.ServiceSpec{
			Selector: sel,
			Type:     apiv1.ServiceTypeClusterIP,
			Ports: []apiv1.ServicePort{
				{
					Name:     "fetcher-port",
					Protocol: apiv1.ProtocolTCP,
					Port:     svcinfo.PortFetcher,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: svcinfo.PortFetcher,
					},
				},
				{
					Name:     "builder-port",
					Protocol: apiv1.ProtocolTCP,
					Port:     svcinfo.PortBuilder,
					TargetPort: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: svcinfo.PortBuilder,
					},
				},
			},
		},
	}
	r.logger.Info("creating builder service", "service_name", name)
	_, err := r.kubernetesClient.CoreV1().Services(ns).Create(ctx, &service, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return &service, nil
}

func (r *EnvironmentReconciler) getBuilderDeploymentList(ctx context.Context, sel map[string]string, ns string) ([]appsv1.Deployment, error) {
	deployList, err := r.kubernetesClient.AppsV1().Deployments(ns).List(
		ctx,
		metav1.ListOptions{
			LabelSelector: labels.Set(sel).AsSelector().String(),
		})
	if err != nil {
		return nil, fmt.Errorf("error getting builder deployment list for namespace %s: %w", ns, err)
	}
	return deployList.Items, nil
}

// builderAuthEnvVars mounts the per-namespace derived builder key onto the
// builder container under dynamic tenancy, so /build verifies with this
// namespace's key and the pod never holds the master. Required for a live tenant
// namespace with auth enabled — the kubelet then gates pod start on the
// controller having provisioned the key (race-free, mirroring the fetcher key);
// optional otherwise, so default-mode and non-tenant pods still start and the
// builder falls back to the master-derived scheme (empty key = pass-through).
func builderAuthEnvVars(namespace string) []apiv1.EnvVar {
	required := utils.PerNamespaceKeyRequired(namespace)
	keyRef := func(name, key string, optional bool) apiv1.EnvVar {
		opt := optional
		return apiv1.EnvVar{Name: name, ValueFrom: &apiv1.EnvVarSource{SecretKeyRef: &apiv1.SecretKeySelector{
			LocalObjectReference: apiv1.LocalObjectReference{Name: fv1.TenantAuthKeysSecret},
			Key:                  key,
			Optional:             &opt,
		}}}
	}
	return []apiv1.EnvVar{
		keyRef("FISSION_BUILDER_KEY", fv1.TenantAuthBuilderKey, !required),
		keyRef("FISSION_BUILDER_KEY_OLD", "builderKeyOld", true),
	}
}

func (r *EnvironmentReconciler) createBuilderDeployment(ctx context.Context, env *fv1.Environment, ns string) (*appsv1.Deployment, error) {
	name := fmt.Sprintf("%v-%v", env.Name, env.ResourceVersion)
	sel := r.getLabels(env.Name, ns, env.ResourceVersion)
	var replicas int32 = 1

	podAnnotations := env.Annotations
	if podAnnotations == nil {
		podAnnotations = make(map[string]string)
	}
	if r.useIstio && env.Spec.AllowAccessToExternalNetwork {
		podAnnotations["sidecar.istio.io/inject"] = "false"
	}

	// Stamp the HMAC key-scheme so buildermgr signs this builder pod's sidecar
	// calls with the key its fetcher will verify with (version-aware signing,
	// builderSigningNamespace). Present for tenant namespaces when per-namespace
	// keys are in use (dynamic or cluster mode), absent otherwise — stable, so at
	// most one rollout when tenancy is first enabled, never ongoing churn.
	if utils.PerNamespaceKeysEnabled() && r.nsResolver.IsTenant(ns) {
		podAnnotations[fv1.AuthKeySchemeAnnotation] = fv1.AuthKeySchemeNamespace
	}

	container, err := util.MergeContainer(&apiv1.Container{
		Name:                   fv1.BuilderContainerName,
		Image:                  env.Spec.Builder.Image,
		ImagePullPolicy:        r.builderImagePullPolicy,
		TerminationMessagePath: "/dev/termination-log",
		Command:                []string{"/builder", r.fetcherConfig.SharedMountPath()},
		// Per-namespace builder key for /build verification under dynamic tenancy
		// (absent/empty otherwise = master-derived or pass-through, unchanged).
		Env: builderAuthEnvVars(ns),
		ReadinessProbe: &apiv1.Probe{
			InitialDelaySeconds: 5,
			PeriodSeconds:       2,
			ProbeHandler: apiv1.ProbeHandler{
				HTTPGet: &apiv1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: svcinfo.PortBuilder,
					},
				},
			},
		},
	}, env.Spec.Builder.Container)
	if err != nil {
		return nil, err
	}

	// AutomountServiceAccountToken=false stops Kubernetes from injecting
	// the fission-builder ServiceAccount token into every container in
	// the pod. The fetcher sidecar re-mounts the token via the projected
	// volume defined below — the user-supplied builder container does
	// not. See GHSA-8wcj-mfrc-jx5q (the buildermgr sibling of
	// GHSA-85g2-pmrx-r49q).
	automountSAToken := false
	pod := apiv1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      sel,
			Annotations: podAnnotations,
		},
		Spec: apiv1.PodSpec{
			Containers:                   []apiv1.Container{*container},
			ServiceAccountName:           fv1.FissionBuilderSA,
			AutomountServiceAccountToken: &automountSAToken,
			Volumes: []apiv1.Volume{
				util.FetcherSATokenProjectedVolume(),
			},
		},
	}

	if r.podSpecPatch != nil {
		// Merge into a deep copy: MergePodSpec mutates its first argument
		// in place even on error (it joins partial errors and keeps the
		// fields that did merge cleanly). Passing pod.Spec directly would
		// leave the deployment with partial patch mutations applied on a
		// log-and-continue error path. The copy is discarded on failure
		// so pod.Spec retains its pre-merge state.
		srcCopy := pod.Spec.DeepCopy()
		updatedPodSpec, err := util.MergePodSpec(srcCopy, r.podSpecPatch)
		if err == nil {
			pod.Spec = *updatedPodSpec
		} else {
			r.logger.Error(err, "Failed to merge the specs")
		}
		// Re-clamp after the merge: MergePodSpec propagates a non-nil
		// AutomountServiceAccountToken from the patch, which would
		// otherwise re-enable the kubelet auto-mount on the user-supplied
		// builder container. See GHSA-8wcj-mfrc-jx5q.
		pod.Spec.AutomountServiceAccountToken = new(false)
	}

	pod.Spec = *(util.ApplyImagePullSecret(env.Spec.ImagePullSecret, pod.Spec))

	var ownerReferences []metav1.OwnerReference
	if r.enableOwnerReferences {
		ownerReferences = []metav1.OwnerReference{
			*metav1.NewControllerRef(env, fv1.SchemeGroupVersion.WithKind("Environment")),
		}
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       ns,
			Name:            name,
			Labels:          sel,
			OwnerReferences: ownerReferences,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: sel,
			},
			Template: pod,
		},
	}

	err = r.fetcherConfig.AddFetcherToPodSpec(&deployment.Spec.Template.Spec, "builder", ns)
	if err != nil {
		return nil, err
	}

	if env.Spec.Builder.PodSpec != nil {
		newPodSpec, err := util.MergePodSpec(&deployment.Spec.Template.Spec, env.Spec.Builder.PodSpec)
		if err != nil {
			return nil, err
		}
		deployment.Spec.Template.Spec = *newPodSpec
		// Re-clamp after the merge: MergePodSpec propagates a non-nil
		// AutomountServiceAccountToken from env.Spec.Builder.PodSpec,
		// which would otherwise re-enable the kubelet auto-mount on the
		// user-supplied builder container. See GHSA-8wcj-mfrc-jx5q.
		deployment.Spec.Template.Spec.AutomountServiceAccountToken = new(false)
	}

	// Re-mount the fission-builder SA token at the canonical Kubernetes
	// path on the fetcher container only. The pod-level
	// AutomountServiceAccountToken=false flag set above suppresses the
	// implicit mount on every container, including fetcher, so we add it
	// back explicitly here. This must run AFTER the
	// env.Spec.Builder.PodSpec merge — MergePodSpec can append additional
	// volumeMounts to the fetcher container, including one at this same
	// path, and kubelet would reject the pod with a duplicate-mount-path
	// error. The helper strips any pre-existing mount at the path before
	// adding its own, so running it last guarantees a single mount on
	// the fetcher container backed by the projected SA token volume.
	// See GHSA-8wcj-mfrc-jx5q.
	if err := util.MountFetcherSATokenOnFetcher(&deployment.Spec.Template.Spec); err != nil {
		return nil, err
	}

	_, err = r.kubernetesClient.AppsV1().Deployments(ns).Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	r.logger.Info("creating builder deployment", "deployment", name)

	return deployment, nil
}
