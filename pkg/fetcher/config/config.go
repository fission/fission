// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fetcher"
	"github.com/fission/fission/pkg/svcinfo"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/otel"
)

type Config struct {
	fetcherImage           string
	fetcherImagePullPolicy apiv1.PullPolicy

	resourceRequirements apiv1.ResourceRequirements

	// used by generic pool when creating env deployment to specify the share volume path for fetcher & env
	// change this may break v1 compatibility, since most of the v1 environments have hard-coded "/userfunc" in loading path
	sharedMountPath  string
	sharedSecretPath string
	sharedCfgMapPath string

	serviceAccount string

	// insecureRegistries is the comma-separated host allowlist permitted to
	// serve OCI package images over plain HTTP (RFC-0001). Forwarded to the
	// fetcher container verbatim; empty (the default) means every registry
	// must serve TLS.
	insecureRegistries string
}

// internalAuthEnvVars returns the env-var entries that mount the
// HMAC-shared-secret values from the chart-installed Secret/fission-internal-auth
// onto the fetcher sidecar container. Every key is marked optional so a
// pod still admits when the chart's internalAuth is disabled.
//
// The fetcher binary calls the storagesvc client, which reads
// FISSION_INTERNAL_AUTH_SECRET from its environment and signs every
// outbound request when set. Without these env vars the fetcher's
// uploads fail with HTTP 401 once storagesvc starts enforcing
// signatures. See docs/internal-auth/00-design.md.
//
// FISSION_FETCHER_KEY / FISSION_STORAGE_KEY are the per-namespace derived keys
// the tenant controller writes into a tenant namespace's TenantAuthKeysSecret
// under multi-namespace tenancy (the master never lands there). The Secret is a
// DIFFERENT name from the chart's master copy so an existing install's
// already-replicated master Secret cannot shadow it.
//
// In the default single-namespace mode all six are optional, so a pod admits
// whether or not internalAuth is configured — byte-identical to before. Under
// dynamic tenancy with auth enabled the fetcher key is REQUIRED: the kubelet then
// blocks pod start until the controller has provisioned the namespace's derived
// key, so a running pod is guaranteed to hold the key the executor will
// version-aware-sign it with — closing the stamp-before-key race without 401s.
// The storage key stays optional even then: storagesvc dual-accepts a
// master-derived signature, so an unprovisioned fetcher degrades gracefully.
func internalAuthEnvVars(namespace string) []apiv1.EnvVar {
	const chartSecret = "fission-internal-auth"
	keysSecret := fv1.TenantAuthKeysSecret

	// Require the fetcher key only where it is guaranteed to be provisioned (a
	// live tenant namespace under dynamic tenancy with auth enabled); elsewhere it
	// stays optional and the fetcher falls back to the master-derived scheme. See
	// utils.PerNamespaceKeyRequired.
	fetcherKeyRequired := utils.PerNamespaceKeyRequired(namespace)

	return []apiv1.EnvVar{
		secretKeyEnv("FISSION_INTERNAL_AUTH_SECRET", chartSecret, "secret", true),
		secretKeyEnv("FISSION_INTERNAL_AUTH_SECRET_OLD", chartSecret, "oldSecret", true),
		secretKeyEnv("FISSION_FETCHER_KEY", keysSecret, fv1.TenantAuthFetcherKey, !fetcherKeyRequired),
		secretKeyEnv("FISSION_FETCHER_KEY_OLD", keysSecret, "fetcherKeyOld", true),
		secretKeyEnv("FISSION_STORAGE_KEY", keysSecret, fv1.TenantAuthStorageKey, true),
		secretKeyEnv("FISSION_STORAGE_KEY_OLD", keysSecret, "storageKeyOld", true),
	}
}

// secretKeyEnv builds a secretKeyRef env var. optional=true lets the pod admit
// when the key (or the whole Secret) is absent — the backwards-compatible
// default; optional=false makes the kubelet gate pod start on the key's presence.
func secretKeyEnv(name, secretName, secretKey string, optional bool) apiv1.EnvVar {
	opt := optional
	return apiv1.EnvVar{
		Name: name,
		ValueFrom: &apiv1.EnvVarSource{
			SecretKeyRef: &apiv1.SecretKeySelector{
				LocalObjectReference: apiv1.LocalObjectReference{Name: secretName},
				Key:                  secretKey,
				Optional:             &opt,
			},
		},
	}
}

func getFetcherResources() (apiv1.ResourceRequirements, error) {
	resourceReqs := apiv1.ResourceRequirements{
		Requests: map[apiv1.ResourceName]resource.Quantity{},
		Limits:   map[apiv1.ResourceName]resource.Quantity{},
	}
	var errs error
	errs = errors.Join(errs,
		parseResources("FETCHER_MINCPU", resourceReqs.Requests, apiv1.ResourceCPU),
		parseResources("FETCHER_MINMEM", resourceReqs.Requests, apiv1.ResourceMemory),
		parseResources("FETCHER_MAXCPU", resourceReqs.Limits, apiv1.ResourceCPU),
		parseResources("FETCHER_MAXMEM", resourceReqs.Limits, apiv1.ResourceMemory),
	)
	return resourceReqs, errs
}

func parseResources(env string, resourceReqs map[apiv1.ResourceName]resource.Quantity, resName apiv1.ResourceName) error {
	val := os.Getenv(env)
	if len(val) > 0 {
		quantity, err := resource.ParseQuantity(val)
		if err != nil {
			return err
		}
		resourceReqs[resName] = quantity
	}
	return nil
}

func MakeFetcherConfig(sharedMountPath string) (*Config, error) {
	resources, err := getFetcherResources()
	if err != nil {
		return nil, err
	}

	fetcherImage := os.Getenv("FETCHER_IMAGE")
	if len(fetcherImage) == 0 {
		fetcherImage = "ghcr.io/fission/fetcher"
	}

	fetcherImagePullPolicy := os.Getenv("FETCHER_IMAGE_PULL_POLICY")
	if len(fetcherImagePullPolicy) == 0 {
		fetcherImagePullPolicy = "IfNotPresent"
	}

	return &Config{
		resourceRequirements:   resources,
		fetcherImage:           fetcherImage,
		fetcherImagePullPolicy: utils.GetImagePullPolicy(fetcherImagePullPolicy),
		sharedMountPath:        sharedMountPath,
		sharedSecretPath:       "/secrets",
		sharedCfgMapPath:       "/configs",
		serviceAccount:         fv1.FissionFetcherSA,
		insecureRegistries:     os.Getenv("FETCHER_ALLOW_INSECURE_REGISTRIES"),
	}, nil
}

func (cfg *Config) SharedMountPath() string {
	return cfg.sharedMountPath
}

// TargetFilenameDeployArchive is the fixed store-path name for v2+
// environments (except AllowedFunctionsPerContainerInfinite, which keys by
// function UID). The poolmgr image-volume path relies on it being
// function-independent to mount one code image per pool.
const TargetFilenameDeployArchive = "deployarchive"

// TargetFilename is the name (under the shared mount path) the fetcher
// stores a function's deployment package at, and therefore the path the
// loader reads. Exposed so the image-volume path (RFC-0001 Path B) can mount
// the package image at exactly the fetcher's store path, turning the
// fetcher's exists-early-exit into a no-op fetch.
func (cfg *Config) TargetFilename(fn *fv1.Function, env *fv1.Environment) string {
	if env.Spec.Version >= 2 {
		if env.Spec.AllowedFunctionsPerContainer == fv1.AllowedFunctionsPerContainerInfinite {
			// workflow loads multiple functions into one function pod,
			// we have to use a Function UID to separate the function code
			// to avoid overwriting.
			return string(fn.UID)
		}
		// set target file name to fix pattern for easy accessing.
		return TargetFilenameDeployArchive
	}
	return "user"
}

func (cfg *Config) NewSpecializeRequest(fn *fv1.Function, env *fv1.Environment) fetcher.FunctionSpecializeRequest {
	targetFilename := cfg.TargetFilename(fn, env)

	return fetcher.FunctionSpecializeRequest{
		FetchReq: fetcher.FunctionFetchRequest{
			FetchType: fv1.FETCH_DEPLOYMENT,
			Package: metav1.ObjectMeta{
				Namespace:       fn.Spec.Package.PackageRef.Namespace,
				Name:            fn.Spec.Package.PackageRef.Name,
				ResourceVersion: fn.Spec.Package.PackageRef.ResourceVersion,
			},
			Filename:    targetFilename,
			Secrets:     fn.Spec.Secrets,
			ConfigMaps:  fn.Spec.ConfigMaps,
			KeepArchive: env.Spec.KeepArchive,
		},
		LoadReq: fetcher.FunctionLoadRequest{
			FilePath:         filepath.Join(cfg.sharedMountPath, targetFilename),
			FunctionName:     fn.Spec.Package.FunctionName,
			FunctionMetadata: &fn.ObjectMeta,
			EnvVersion:       env.Spec.Version,
			StateKeyspace:    stateKeyspace(fn),
		},
	}
}

// stateKeyspace resolves the RFC-0023 keyspace for a stateful function
// ("" = not opted in). Only this non-secret name travels in the specialize
// request; the fetcher derives the actual token pod-locally.
func stateKeyspace(fn *fv1.Function) string {
	if fn.Spec.State == nil {
		return ""
	}
	return fn.Spec.State.EffectiveKeyspace(fn.Name)
}

// AddFetcherToPodSpec adds the fetcher sidecar to podSpec. namespace is where the
// pod will run; it scopes whether the per-namespace derived key is required (see
// internalAuthEnvVars).
func (cfg *Config) AddFetcherToPodSpec(podSpec *apiv1.PodSpec, mainContainerName, namespace string) error {
	return cfg.addFetcherToPodSpecWithCommand(podSpec, mainContainerName, namespace, cfg.fetcherCommand())
}

func (cfg *Config) AddSpecializingFetcherToPodSpec(podSpec *apiv1.PodSpec, mainContainerName, namespace string, fn *fv1.Function, env *fv1.Environment) error {
	specializeReq := cfg.NewSpecializeRequest(fn, env)
	specializePayload, err := json.Marshal(specializeReq)
	if err != nil {
		return err
	}

	return cfg.addFetcherToPodSpecWithCommand(
		podSpec,
		mainContainerName,
		namespace,
		cfg.fetcherCommand(
			"-specialize-on-startup",
			"-specialize-request", string(specializePayload),
		),
	)
}

func (cfg *Config) fetcherCommand(extraArgs ...string) []string {
	command := []string{"/fetcher",
		"-secret-dir", cfg.sharedSecretPath,
		"-cfgmap-dir", cfg.sharedCfgMapPath,
	}

	command = append(command, extraArgs...)
	command = append(command, cfg.sharedMountPath)
	return command
}

func (cfg *Config) volumesWithMounts() ([]apiv1.Volume, []apiv1.VolumeMount) {

	items := make([]apiv1.DownwardAPIVolumeFile, 0)
	podNameFieldSelector := apiv1.ObjectFieldSelector{
		FieldPath: "metadata.name",
	}

	podNamespaceFieldSelector := apiv1.ObjectFieldSelector{
		FieldPath: "metadata.namespace",
	}
	podName := apiv1.DownwardAPIVolumeFile{
		Path:     "name",
		FieldRef: &podNameFieldSelector,
	}

	podNamespace := apiv1.DownwardAPIVolumeFile{
		Path:     "namespace",
		FieldRef: &podNamespaceFieldSelector,
	}

	items = append(items, podName, podNamespace)
	dwAPIVol := apiv1.DownwardAPIVolumeSource{Items: items}
	volumes := []apiv1.Volume{
		{
			Name: fv1.SharedVolumeUserfunc,
			VolumeSource: apiv1.VolumeSource{
				EmptyDir: &apiv1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: fv1.SharedVolumeSecrets,
			VolumeSource: apiv1.VolumeSource{
				EmptyDir: &apiv1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: fv1.SharedVolumeConfigmaps,
			VolumeSource: apiv1.VolumeSource{
				EmptyDir: &apiv1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: fv1.PodInfoVolume,
			VolumeSource: apiv1.VolumeSource{
				DownwardAPI: &dwAPIVol,
			},
		},
	}
	mounts := []apiv1.VolumeMount{
		{
			Name:      fv1.SharedVolumeUserfunc,
			MountPath: cfg.sharedMountPath,
		},
		{
			Name:      fv1.SharedVolumeSecrets,
			MountPath: cfg.sharedSecretPath,
		},
		{
			Name:      fv1.SharedVolumeConfigmaps,
			MountPath: cfg.sharedCfgMapPath,
		},
		{
			Name:      fv1.PodInfoVolume,
			MountPath: fv1.PodInfoMount,
		},
	}

	return volumes, mounts
}

func (cfg *Config) addFetcherToPodSpecWithCommand(podSpec *apiv1.PodSpec, mainContainerName, namespace string, command []string) error {
	volumes, mounts := cfg.volumesWithMounts()
	c := apiv1.Container{
		Name:                   "fetcher",
		Command:                command,
		Image:                  cfg.fetcherImage,
		ImagePullPolicy:        cfg.fetcherImagePullPolicy,
		TerminationMessagePath: "/dev/termination-log",
		VolumeMounts:           mounts,
		Resources:              cfg.resourceRequirements,
		ReadinessProbe: &apiv1.Probe{
			InitialDelaySeconds: 1,
			PeriodSeconds:       1,
			FailureThreshold:    30,
			ProbeHandler: apiv1.ProbeHandler{
				HTTPGet: &apiv1.HTTPGetAction{
					Path: "/readiness-healthz",
					Port: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: svcinfo.PortFetcher,
					},
				},
			},
		},
		LivenessProbe: &apiv1.Probe{
			InitialDelaySeconds: 1,
			PeriodSeconds:       5,
			ProbeHandler: apiv1.ProbeHandler{
				HTTPGet: &apiv1.HTTPGetAction{
					Path: "/healthz",
					Port: intstr.IntOrString{
						Type:   intstr.Int,
						IntVal: svcinfo.PortFetcher,
					},
				},
			},
		},
		Env: append(otel.OtelEnvForContainer(), internalAuthEnvVars(namespace)...),
	}
	if cfg.insecureRegistries != "" {
		c.Env = append(c.Env, apiv1.EnvVar{
			Name:  "FETCHER_ALLOW_INSECURE_REGISTRIES",
			Value: cfg.insecureRegistries,
		})
	}

	// Connection-draining preStop hook; see utils.DrainLifecycle. Must be
	// the kubelet-native sleep action — the fetcher image is distroless
	// (chainguard/static) and has no /bin/sleep to exec.
	if podSpec.TerminationGracePeriodSeconds != nil {
		c.Lifecycle = utils.DrainLifecycle(*podSpec.TerminationGracePeriodSeconds)
	}

	found := false
	for ix, container := range podSpec.Containers {
		if container.Name != mainContainerName {
			continue
		}

		found = true
		container.VolumeMounts = append(container.VolumeMounts, mounts...)
		podSpec.Containers[ix] = container
	}
	if !found {
		existingContainerNames := make([]string, 0, len(podSpec.Containers))
		for _, existingContainer := range podSpec.Containers {
			existingContainerNames = append(existingContainerNames, existingContainer.Name)
		}
		return fmt.Errorf("could not find main container '%s' in given PodSpec. Found: %v",
			mainContainerName,
			existingContainerNames)
	}

	podSpec.Volumes = append(podSpec.Volumes, volumes...)
	podSpec.Containers = append(podSpec.Containers, c)
	if podSpec.ServiceAccountName == "" {
		podSpec.ServiceAccountName = fv1.FissionFetcherSA
	}

	return nil
}
