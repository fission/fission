// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package logger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils"
)

// loggerScheme registers the Kubernetes built-in types (Pods) the logger's
// reconciler watches.
var loggerScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(loggerScheme))
}

var nodeName = os.Getenv("NODE_NAME")

// These are vars (not consts) so tests can redirect them to a temp directory;
// in production they are never reassigned.
var (
	originalContainerLogPath = "/var/log/containers"
	fissionSymlinkPath       = "/var/log/fission"
)

// podReconciler creates log symlinks for valid, ready function pods scheduled on
// this node, replacing the Pod informer's Add/Update handlers. It reacts to pod
// STATUS changes (container IDs appear in Status.ContainerStatuses), so it must
// NOT use GenerationChangedPredicate — that drops status-only updates and the
// symlinks would never be created. A delete needs no handling here: the
// symlinkReaper goroutine recycles orphan symlinks, so a NotFound (the pod is
// gone) is a no-op, mirroring the old DeleteFunc.
type podReconciler struct {
	logger logr.Logger
	client client.Client
}

func (r *podReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	pod := &corev1.Pod{}
	if err := r.client.Get(ctx, req.NamespacedName, pod); err != nil {
		if apierrors.IsNotFound(err) {
			// Pod gone; symlinkReaper reaps any orphan symlink.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	// Mirror the old Add/Update handlers: only act on valid function pods on this
	// node that are ready. The cache is already scoped to this node by field
	// selector when NODE_NAME is set, but isValidFunctionPodOnNode re-checks the
	// node defensively (and is the only filter when NODE_NAME is unset).
	if !isValidFunctionPodOnNode(pod) || !utils.IsReadyPod(pod) {
		return ctrl.Result{}, nil
	}
	if err := createLogSymlinks(r.logger, pod); err != nil {
		r.logger.Error(err, "error creating symlink", "function", pod.Labels[fv1.FUNCTION_NAME])
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func createLogSymlinks(zapLogger logr.Logger, pod *corev1.Pod) error {
	for _, container := range pod.Status.ContainerStatuses {
		containerUID, err := parseContainerString(container.ContainerID)
		if err != nil {
			zapLogger.Error(err, "error parsing container uid",
				"container", container.Name,
				"pod", pod.Name,
				"namespace", pod.Namespace)
			continue
		}
		containerLogPath := getLogPath(originalContainerLogPath, pod.Name, pod.Namespace, container.Name, containerUID)
		symlinkLogPath := getLogPath(fissionSymlinkPath, pod.Name, pod.Namespace, container.Name, containerUID)

		// check whether a symlink exists, if yes then ignore it
		if _, err := os.Stat(symlinkLogPath); os.IsNotExist(err) {
			err := os.Symlink(containerLogPath, symlinkLogPath)
			if err != nil {
				zapLogger.Error(err, "error creating symlink",
					"container", container.Name,
					"pod", pod.Name,
					"namespace", pod.Namespace)
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
func symlinkReaper(zapLogger logr.Logger) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		reapStaleSymlinks(zapLogger, fissionSymlinkPath)
	}
}

// reapStaleSymlinks walks dir once and removes any symlink whose target no
// longer exists. Split out from symlinkReaper's ticker loop so the reaping
// logic can be unit-tested without waiting on the timer.
func reapStaleSymlinks(zapLogger logr.Logger, dir string) {
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Log and keep walking the rest of the tree rather than aborting
			// the whole reap on one unreadable entry.
			zapLogger.Error(err, "error walking symlink dir, skipping entry", "filepath", path)
			return nil
		}
		if target, e := os.Readlink(path); e == nil {
			if _, pathErr := os.Stat(target); os.IsNotExist(pathErr) {
				zapLogger.V(1).Info("remove symlink file", "filepath", path)
				if rmErr := os.Remove(path); rmErr != nil {
					zapLogger.Error(rmErr, "error removing stale symlink", "filepath", path)
				}
			}
		}
		return nil
	})
	if err != nil {
		zapLogger.Error(err, "error reaping symlink")
	}
}

func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger) error {
	if _, err := os.Stat(fissionSymlinkPath); os.IsNotExist(err) {
		logger.Info("symlink path not exist, create it",
			"fissionSymlinkPath", fissionSymlinkPath)
		// 0o755 is required so node-level log shippers (fluentd, fluent-bit,
		// promtail) running as different UIDs can traverse this directory
		// to read function pod log symlinks. Tighter modes break log
		// aggregation; the contents themselves are non-sensitive symlinks.
		err = os.Mkdir(fissionSymlinkPath, 0o755)
		if err != nil {
			logger.Error(err, "error creating fissionSymlinkPath")

			os.Exit(1)
		}
	}
	go symlinkReaper(logger)

	restConfig, err := clientGen.GetRestConfig()
	if err != nil {
		return err
	}

	// The logger is a DaemonSet (one pod per node). Each node's logger MUST
	// process its OWN pods, so the Manager MUST NOT use leader election — with
	// leader election only one node's logger would run and logging would break on
	// every other node. Build a non-leader-elected Manager whose every runnable
	// (here the Pod reconciler) runs on this replica unconditionally.
	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme: loggerScheme,
		Cache:  loggerCacheOptions(),
		// No Manager-owned metrics/health servers: the logger serves none.
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
		Logger:                 logger,
	})
	if err != nil {
		return fmt.Errorf("unable to set up logger manager: %w", err)
	}

	pr := &podReconciler{
		logger: logger.WithName("pod_reconciler"),
		client: mgr.GetClient(),
	}
	// React to pod STATUS changes (container IDs appear in status), so do NOT use
	// GenerationChangedPredicate. Pass no predicates to watch every pod event.
	if err := controller.RegisterWithPredicates(mgr, &corev1.Pod{}, pr, "logger-pod", 0); err != nil {
		return fmt.Errorf("unable to register pod reconciler: %w", err)
	}

	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("error running logger manager: %w", err)
	}

	logger.Error(nil, "Stop watching pod changes")
	return nil
}

// loggerCacheOptions scopes the Manager's Pod cache to pods on this node when
// NODE_NAME is set (a spec.nodeName field selector), so the per-node DaemonSet
// pod only mirrors its own node's pods rather than every pod in the cluster —
// matching the per-node filtering isValidFunctionPodOnNode applied. When
// NODE_NAME is unset (e.g. tests, or an install that doesn't inject it) the
// cache watches all pods and isValidFunctionPodOnNode filters in the reconciler.
// The cache is cluster-wide (DefaultNamespaces unset) because function pods can
// live in any namespace; the logger's RBAC already grants pods list/watch.
func loggerCacheOptions() crcache.Options {
	if nodeName == "" {
		return crcache.Options{}
	}
	return crcache.Options{
		ByObject: map[client.Object]crcache.ByObject{
			&corev1.Pod{}: {
				Field: fields.OneTermEqualSelector("spec.nodeName", nodeName),
			},
		},
	}
}
