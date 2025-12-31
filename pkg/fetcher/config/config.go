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
	}, nil
}

func (cfg *Config) SharedMountPath() string {
	return cfg.sharedMountPath
}

func (cfg *Config) NewSpecializeRequest(fn *fv1.Function, env *fv1.Environment) fetcher.FunctionSpecializeRequest {
	targetFilename := "user"
	if env.Spec.Version >= 2 {
		if env.Spec.AllowedFunctionsPerContainer == fv1.AllowedFunctionsPerContainerInfinite {
			// workflow loads multiple functions into one function pod,
			// we have to use a Function UID to separate the function code
			// to avoid overwriting.
			targetFilename = string(fn.UID)
		} else {
			// set target file name to fix pattern for
			// easy accessing.
			targetFilename = "deployarchive"
		}
	}

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
		},
	}
}

func (cfg *Config) AddFetcherToPodSpec(podSpec *apiv1.PodSpec, mainContainerName string) error {
	return cfg.addFetcherToPodSpecWithCommand(podSpec, mainContainerName, cfg.fetcherCommand())
}

func (cfg *Config) AddSpecializingFetcherToPodSpec(podSpec *apiv1.PodSpec, mainContainerName string, fn *fv1.Function, env *fv1.Environment) error {
	specializeReq := cfg.NewSpecializeRequest(fn, env)
	specializePayload, err := json.Marshal(specializeReq)
	if err != nil {
		return err
	}

	return cfg.addFetcherToPodSpecWithCommand(
		podSpec,
		mainContainerName,
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

func (cfg *Config) addFetcherToPodSpecWithCommand(podSpec *apiv1.PodSpec, mainContainerName string, command []string) error {
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
						IntVal: 8000,
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
						IntVal: 8000,
					},
				},
			},
		},
		Env: otel.OtelEnvForContainer(),
	}

	// Pod is removed from endpoints list for service when it's
	// state became "Termination". We used preStop hook as the
	// workaround for connection draining since pod maybe shutdown
	// before grace period expires.
	// https://kubernetes.io/docs/concepts/workloads/pods/pod/#termination-of-pods
	// https://github.com/kubernetes/kubernetes/issues/47576#issuecomment-308900172
	if podSpec.TerminationGracePeriodSeconds != nil {
		c.Lifecycle = &apiv1.Lifecycle{
			PreStop: &apiv1.LifecycleHandler{
				Exec: &apiv1.ExecAction{
					Command: []string{
						"/bin/sleep",
						fmt.Sprintf("%v", *podSpec.TerminationGracePeriodSeconds),
					},
				},
			},
		}
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
