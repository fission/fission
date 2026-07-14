// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/utils"
)

const (
	dumpFileName string = "fission-dump"

	// adoptConcurrency bounds how many functions AdoptFunctions (re)creates in
	// parallel, so the startup adopt sweep doesn't fan out one goroutine — and
	// a burst of API calls — per function on clusters with many functions.
	adoptConcurrency = 10
)

// AdoptFunctions lists every Function of executorType across the executor's
// resource namespaces and (re)creates it via create, concurrently — the shared
// body of the newdeploy and container managers' AdoptExistingResources. It runs
// at executor startup to re-stamp pre-existing objects with the new instance ID;
// pass the executor type's *throttled* createFunction so adopt single-flights
// with the Function reconciler rather than racing it. A list error in one
// namespace is logged and skipped rather than aborting the whole sweep.
func AdoptFunctions(ctx context.Context, logger logr.Logger, fissionClient versioned.Interface,
	executorType fv1.ExecutorType, create func(context.Context, *fv1.Function) error,
) {
	g := new(errgroup.Group)
	g.SetLimit(adoptConcurrency)
	for _, namespace := range utils.DefaultNSResolver().FissionResourceNamespaces() {
		fnList, err := fissionClient.CoreV1().Functions(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			logger.Error(err, "error listing functions to adopt", "namespace", namespace)
			continue
		}
		for i := range fnList.Items {
			fn := &fnList.Items[i]
			if fn.Spec.InvokeStrategy.ExecutionStrategy.ExecutorType != executorType {
				continue
			}
			// Each adopt is independent; log and continue rather than
			// returning an error, so one function's failure neither cancels
			// the others nor aborts the sweep.
			g.Go(func() error {
				if err := create(ctx, fn); err != nil {
					logger.Error(err, "failed to adopt resources for function",
						"function", fn.Name, "namespace", fn.Namespace)
					return nil
				}
				logger.Info("adopted resources for function", "function", fn.Name, "namespace", fn.Namespace)
				return nil
			})
		}
	}
	_ = g.Wait()
}

// ApplyImagePullSecret applies image pull secret to the give pod spec.
// It's intentional not to check the existence of secret here.
// First, Kubernetes will set Pod status to "ImagePullBackOff" once
// kubelet failed to pull image so that users will know what's happening.
// Second, Fission no longer need to handle "secret not found" error
// when creating the environment deployment since kubelet will retry to
// pull image until successes.
func ApplyImagePullSecret(secret string, podspec apiv1.PodSpec) *apiv1.PodSpec {
	if len(secret) > 0 {
		podspec.ImagePullSecrets = []apiv1.LocalObjectReference{{Name: secret}}
	}
	return &podspec
}

// WaitTimeout starts a wait group with timeout
func WaitTimeout(wg *sync.WaitGroup, timeout time.Duration) {
	waitCh := make(chan struct{})
	go func() {
		defer close(waitCh)
		wg.Wait()
	}()
	select {
	case <-waitCh:
	case <-time.After(timeout):
	}
}

// ConvertConfigSecrets returns envFromSource which can be passed directly into the pod spec
func ConvertConfigSecrets(ctx context.Context, fn *fv1.Function, kc kubernetes.Interface) ([]apiv1.EnvFromSource, error) {
	cmList := fn.Spec.ConfigMaps
	secList := fn.Spec.Secrets
	cmEnvSources := make([]*apiv1.ConfigMapEnvSource, 0)
	secEnvSources := make([]*apiv1.SecretEnvSource, 0)
	for _, cm := range cmList {
		if cm.Namespace != fn.Namespace {
			return nil, errors.New("function should not reference config map of different namespace")
		}
		_, err := kc.CoreV1().ConfigMaps(cm.Namespace).Get(ctx, cm.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}

		cmEnvSource := &apiv1.ConfigMapEnvSource{
			LocalObjectReference: apiv1.LocalObjectReference{Name: cm.Name},
		}

		cmEnvSources = append(cmEnvSources, cmEnvSource)
	}

	for _, sec := range secList {
		if sec.Namespace != fn.Namespace {
			return nil, errors.New("function should not reference secret of different namespace")
		}
		_, err := kc.CoreV1().Secrets(sec.Namespace).Get(ctx, sec.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}

		secEnvSource := &apiv1.SecretEnvSource{
			LocalObjectReference: apiv1.LocalObjectReference{Name: sec.Name},
		}

		secEnvSources = append(secEnvSources, secEnvSource)
	}

	envFromSources := make([]apiv1.EnvFromSource, 0)
	for _, cmEnvSource := range cmEnvSources {
		envFromSource := apiv1.EnvFromSource{
			ConfigMapRef: cmEnvSource,
		}
		envFromSources = append(envFromSources, envFromSource)
	}

	for _, secEnvSource := range secEnvSources {
		envFromSource := apiv1.EnvFromSource{
			SecretRef: secEnvSource,
		}
		envFromSources = append(envFromSources, envFromSource)
	}
	return envFromSources, nil
}

func GetSpecFromConfigMap(filePath string) (*apiv1.PodSpec, error) {
	// check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return nil, err
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("error reading YAML file %s: %w", filePath, err)
	}
	additionalSpec := &apiv1.PodSpec{}
	err = yaml.UnmarshalStrict(content, &additionalSpec)
	return additionalSpec, err
}

func GetObjectReaperInterval(logger logr.Logger, executorType fv1.ExecutorType, defaultReaperInterval uint) uint {
	// TODO think about migration to executor package as const.
	globalEnvVarName := "OBJECT_REAPER_INTERVAL"

	executorTypeEnvVarName := getExecutorEnvVarName(executorType)
	keys := []string{executorTypeEnvVarName, globalEnvVarName}
	for _, k := range keys {
		interval, err := utils.GetUIntValueFromEnv(k)
		if err != nil {
			logger.V(1).Info(fmt.Sprintf("Failed to parse %s", k))
		} else {
			return interval
		}
	}

	return defaultReaperInterval
}

func getExecutorEnvVarName(executor fv1.ExecutorType) string {
	return strings.ToUpper(string(executor)) + "_OBJECT_REAPER_INTERVAL"
}

// CreateDumpFile => create dump file inside temp directory
func CreateDumpFile(logger logr.Logger) (*os.File, error) {
	dumpPath := os.TempDir()
	logger.Info("creating dump file", "dump_path", dumpPath)

	return os.Create(fmt.Sprintf("%s/%s-%d.txt", dumpPath, dumpFileName, time.Now().Unix()))
}

// DoesContainerExistInPodSpec checks if the container with the given name exists in the pod spec
func DoesContainerExistInPodSpec(containerName string, podSpec *apiv1.PodSpec) bool {
	for _, container := range podSpec.Containers {
		if container.Name == containerName {
			return true
		}
	}
	return false
}

// AtoiOr fetches Int value from envvar. If not set, returns def value
func AtoiOr(envVar string, def int) int {
	got := os.Getenv(envVar)
	output, err := strconv.Atoi(got)
	if err != nil {
		return def
	}
	return output
}

// DurOr fetches time.Duration value from envvar. If not set, returns def value
func DurOr(envVar string, def time.Duration) time.Duration {
	got := os.Getenv(envVar)
	output, err := time.ParseDuration(got)
	if err != nil {
		return def
	}
	return output
}
