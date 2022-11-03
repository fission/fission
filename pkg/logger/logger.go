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

package logger

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	k8sInformers "k8s.io/client-go/informers"
	k8sCache "k8s.io/client-go/tools/cache"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils"
)

var nodeName = os.Getenv("NODE_NAME")

const (
	originalContainerLogPath = "/var/log/containers"
	fissionSymlinkPath       = "/var/log/fission"
)

func podInformerHandlers(zapLogger *zap.Logger) k8sCache.ResourceEventHandler {
	return k8sCache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod := obj.(*corev1.Pod)
			if !isValidFunctionPodOnNode(pod) || !utils.IsReadyPod(pod) {
				return
			}
			err := createLogSymlinks(zapLogger, pod)
			if err != nil {
				funcName := pod.Labels[fv1.FUNCTION_NAME]
				zapLogger.Error("error creating symlink",
					zap.String("function", funcName), zap.Error(err))
			}
		},
		UpdateFunc: func(_, obj interface{}) {
			pod := obj.(*corev1.Pod)
			if !isValidFunctionPodOnNode(pod) || !utils.IsReadyPod(pod) {
				return
			}
			err := createLogSymlinks(zapLogger, pod)
			if err != nil {
				funcName := pod.Labels[fv1.FUNCTION_NAME]
				zapLogger.Error("error creating symlink",
					zap.String("function", funcName), zap.Error(err))
			}
		},
		DeleteFunc: func(obj interface{}) {
			// Do nothing here, let symlink reaper to recycle orphan symlink file
		},
	}
}

func createLogSymlinks(zapLogger *zap.Logger, pod *corev1.Pod) error {
	for _, container := range pod.Status.ContainerStatuses {
		containerUID, err := parseContainerString(container.ContainerID)
		if err != nil {
			zapLogger.Error("error parsing container uid",
				zap.String("container", container.Name),
				zap.String("pod", pod.Name),
				zap.String("namespace", pod.Namespace),
				zap.Error(err))
			continue
		}
		containerLogPath := getLogPath(originalContainerLogPath, pod.Name, pod.Namespace, container.Name, containerUID)
		symlinkLogPath := getLogPath(fissionSymlinkPath, pod.Name, pod.Namespace, container.Name, containerUID)

		// check whether a symlink exists, if yes then ignore it
		if _, err := os.Stat(symlinkLogPath); os.IsNotExist(err) {
			err := os.Symlink(containerLogPath, symlinkLogPath)
			if err != nil {
				zapLogger.Error("error creating symlink",
					zap.String("container", container.Name),
					zap.String("pod", pod.Name),
					zap.String("namespace", pod.Namespace),
					zap.Error(err))
			}
		}
	}

	return nil
}

// isValidFunctionPodOnNode checks whether a pod is scheduled to the node the logger runs on
// and examines it's metadata labels to ensure it's a qualified function pod.
func isValidFunctionPodOnNode(pod *corev1.Pod) bool {
	if pod.Spec.NodeName != nodeName {
		return false
	}
	labels := []string{fv1.ENVIRONMENT_NAMESPACE, fv1.ENVIRONMENT_NAME, fv1.ENVIRONMENT_UID,
		fv1.FUNCTION_NAMESPACE, fv1.FUNCTION_NAME, fv1.FUNCTION_UID, fv1.EXECUTOR_TYPE}
	for _, l := range labels {
		if len(pod.Labels[l]) == 0 {
			return false
		}
	}
	return true
}

// The ContainerID is consist of container engine type (docker://) and uuid of container.
// (e.g., docker://f4ca66baaa715030e20273aaf5232635a144165f1cd8e34ca5175064c245b679)
// This function tries to extract container uuid from ContainerID.
func parseContainerString(containerID string) (string, error) {
	// Trim the quotes and split the type and ID.
	parts := strings.Split(strings.Trim(containerID, "\""), "://")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid container ID: %q", containerID)
	}
	_, ID := parts[0], parts[1]
	return ID, nil
}

func getLogPath(pathPrefix, podName, podNamespace, containerName, containerID string) string {
	logName := fmt.Sprintf("%s_%s_%s-%s.log", podName, podNamespace, containerName, containerID)
	return filepath.Join(pathPrefix, logName)
}

// symlinkReaper periodically checks and removes symlink file if it's target container log file is no longer exists.
func symlinkReaper(zapLogger *zap.Logger) {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		err := filepath.Walk(fissionSymlinkPath, func(path string, info os.FileInfo, err error) error {
			if target, e := os.Readlink(path); e == nil {
				if _, pathErr := os.Stat(target); os.IsNotExist(pathErr) {
					zapLogger.Debug("remove symlink file", zap.String("filepath", path))
					os.Remove(path)
				}
			}
			return nil
		})
		if err != nil {
			zapLogger.Error("error reaping symlink", zap.Error(err))
		}
	}
}

func Start(ctx context.Context, logger *zap.Logger) {
	if _, err := os.Stat(fissionSymlinkPath); os.IsNotExist(err) {
		logger.Info("symlink path not exist, create it",
			zap.String("fissionSymlinkPath", fissionSymlinkPath))
		err = os.Mkdir(fissionSymlinkPath, 0755)
		if err != nil {
			logger.Fatal("error creating fissionSymlinkPath", zap.Error(err))
		}
	}
	go symlinkReaper(logger)
	_, kubernetesClient, _, _, err := crd.MakeFissionClient("")
	if err != nil {
		log.Fatalf("Error starting pod watcher: %v", err)
	}
	informerFactory := k8sInformers.NewSharedInformerFactory(kubernetesClient, time.Minute*30)
	podInformer := informerFactory.Core().V1().Pods().Informer()
	podInformer.AddEventHandler(podInformerHandlers(logger))
	podInformer.Run(ctx.Done())
	logger.Error("Stop watching pod changes")
}
