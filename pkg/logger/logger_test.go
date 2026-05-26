// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package logger

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestParseContainerString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		containerID string
		want        string
		wantErr     bool
	}{
		{name: "docker", containerID: "docker://abc123", want: "abc123"},
		{name: "containerd", containerID: "containerd://deadbeef", want: "deadbeef"},
		{name: "cri-o", containerID: "cri-o://feed01", want: "feed01"},
		{name: "quoted", containerID: `"docker://quoted01"`, want: "quoted01"},
		{name: "no scheme separator", containerID: "docker-abc123", wantErr: true},
		{name: "empty", containerID: "", wantErr: true},
		{name: "too many separators", containerID: "docker://a://b", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseContainerString(tt.containerID)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetLogPath(t *testing.T) {
	t.Parallel()
	got := getLogPath("/var/log/containers", "mypod", "myns", "mycontainer", "uid42")
	assert.Equal(t, filepath.Join("/var/log/containers", "mypod_myns_mycontainer-uid42.log"), got)
}

// validFunctionPod returns a pod carrying every label isValidFunctionPodOnNode requires.
func validFunctionPod(node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "func-pod",
			Namespace: "fission-function",
			Labels: map[string]string{
				fv1.ENVIRONMENT_NAMESPACE: "default",
				fv1.ENVIRONMENT_NAME:      "node-env",
				fv1.ENVIRONMENT_UID:       "env-uid",
				fv1.FUNCTION_NAMESPACE:    "default",
				fv1.FUNCTION_NAME:         "hello",
				fv1.FUNCTION_UID:          "fn-uid",
				fv1.EXECUTOR_TYPE:         "poolmgr",
			},
		},
		Spec: corev1.PodSpec{NodeName: node},
	}
}

func TestIsValidFunctionPodOnNode(t *testing.T) {
	// Mutates the package-level nodeName, so keep serial.
	origNode := nodeName
	nodeName = "this-node"
	t.Cleanup(func() { nodeName = origNode })

	t.Run("valid pod on this node", func(t *testing.T) {
		assert.True(t, isValidFunctionPodOnNode(validFunctionPod("this-node")))
	})

	t.Run("scheduled on a different node", func(t *testing.T) {
		assert.False(t, isValidFunctionPodOnNode(validFunctionPod("other-node")))
	})

	t.Run("missing a required label", func(t *testing.T) {
		pod := validFunctionPod("this-node")
		delete(pod.Labels, fv1.FUNCTION_UID)
		assert.False(t, isValidFunctionPodOnNode(pod))
	})

	t.Run("empty required label", func(t *testing.T) {
		pod := validFunctionPod("this-node")
		pod.Labels[fv1.EXECUTOR_TYPE] = ""
		assert.False(t, isValidFunctionPodOnNode(pod))
	})
}

// redirectLogPaths points the package log paths at a temp dir for the duration
// of the test and returns the symlink dir. Mutates package state, so callers
// must not run in parallel.
func redirectLogPaths(t *testing.T) (symlinkDir, containerDir string) {
	t.Helper()
	symlinkDir = filepath.Join(t.TempDir(), "fission")
	containerDir = filepath.Join(t.TempDir(), "containers")
	require.NoError(t, os.MkdirAll(symlinkDir, 0o755))

	origSym, origCont := fissionSymlinkPath, originalContainerLogPath
	fissionSymlinkPath, originalContainerLogPath = symlinkDir, containerDir
	t.Cleanup(func() { fissionSymlinkPath, originalContainerLogPath = origSym, origCont })
	return symlinkDir, containerDir
}

func podWithContainerStatuses(statuses ...corev1.ContainerStatus) *corev1.Pod {
	pod := validFunctionPod("this-node")
	pod.Status.ContainerStatuses = statuses
	return pod
}

func TestCreateLogSymlinks(t *testing.T) {
	t.Run("creates symlinks for valid container statuses", func(t *testing.T) {
		symlinkDir, containerDir := redirectLogPaths(t)
		pod := podWithContainerStatuses(
			corev1.ContainerStatus{Name: "c1", ContainerID: "docker://uid-one"},
			corev1.ContainerStatus{Name: "c2", ContainerID: "containerd://uid-two"},
		)

		require.NoError(t, createLogSymlinks(logr.Discard(), pod))

		for name, uid := range map[string]string{"c1": "uid-one", "c2": "uid-two"} {
			link := getLogPath(symlinkDir, pod.Name, pod.Namespace, name, uid)
			target, err := os.Readlink(link)
			require.NoError(t, err, "symlink for %s should exist", name)
			assert.Equal(t, getLogPath(containerDir, pod.Name, pod.Namespace, name, uid), target)
		}
	})

	t.Run("skips containers with unparseable IDs", func(t *testing.T) {
		symlinkDir, _ := redirectLogPaths(t)
		pod := podWithContainerStatuses(
			corev1.ContainerStatus{Name: "bad", ContainerID: "not-a-valid-id"},
		)

		require.NoError(t, createLogSymlinks(logr.Discard(), pod))

		entries, err := os.ReadDir(symlinkDir)
		require.NoError(t, err)
		assert.Empty(t, entries, "no symlink should be created for an unparseable container ID")
	})

	t.Run("leaves an existing path untouched", func(t *testing.T) {
		symlinkDir, _ := redirectLogPaths(t)
		pod := podWithContainerStatuses(
			corev1.ContainerStatus{Name: "c1", ContainerID: "docker://uid-one"},
		)
		existing := getLogPath(symlinkDir, pod.Name, pod.Namespace, "c1", "uid-one")
		require.NoError(t, os.WriteFile(existing, []byte("pre-existing"), 0o644))

		require.NoError(t, createLogSymlinks(logr.Discard(), pod))

		// Still a regular file (not replaced by a symlink).
		info, err := os.Lstat(existing)
		require.NoError(t, err)
		assert.Zero(t, info.Mode()&os.ModeSymlink, "existing regular file must not be replaced")
	})
}

func TestReapStaleSymlinks(t *testing.T) {
	dir := t.TempDir()

	// A symlink whose target exists should be kept.
	liveTarget := filepath.Join(dir, "live-target.log")
	require.NoError(t, os.WriteFile(liveTarget, []byte("x"), 0o644))
	liveLink := filepath.Join(dir, "live.log")
	require.NoError(t, os.Symlink(liveTarget, liveLink))

	// A symlink whose target is gone should be reaped.
	staleLink := filepath.Join(dir, "stale.log")
	require.NoError(t, os.Symlink(filepath.Join(dir, "does-not-exist.log"), staleLink))

	reapStaleSymlinks(logr.Discard(), dir)

	_, err := os.Lstat(liveLink)
	assert.NoError(t, err, "symlink with a live target should be kept")
	_, err = os.Lstat(staleLink)
	assert.True(t, os.IsNotExist(err), "symlink with a missing target should be removed")
}

func TestPodInformerHandlers(t *testing.T) {
	origNode := nodeName
	nodeName = "this-node"
	t.Cleanup(func() { nodeName = origNode })

	handler := podInformerHandlers(logr.Discard())
	require.NotNil(t, handler)

	readyPod := func() *corev1.Pod {
		pod := podWithContainerStatuses(
			corev1.ContainerStatus{Name: "c1", ContainerID: "docker://ready-uid", Ready: true},
		)
		pod.Status.PodIP = "10.0.0.1"
		return pod
	}

	t.Run("AddFunc creates a symlink for a valid ready pod", func(t *testing.T) {
		symlinkDir, _ := redirectLogPaths(t)
		pod := readyPod()

		handler.OnAdd(pod, false)

		link := getLogPath(symlinkDir, pod.Name, pod.Namespace, "c1", "ready-uid")
		_, err := os.Lstat(link)
		assert.NoError(t, err, "AddFunc should create the symlink")
	})

	t.Run("AddFunc ignores a pod scheduled elsewhere", func(t *testing.T) {
		symlinkDir, _ := redirectLogPaths(t)
		pod := readyPod()
		pod.Spec.NodeName = "other-node"

		handler.OnAdd(pod, false)

		entries, err := os.ReadDir(symlinkDir)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("UpdateFunc creates a symlink for a valid ready pod", func(t *testing.T) {
		symlinkDir, _ := redirectLogPaths(t)
		pod := readyPod()

		handler.OnUpdate(nil, pod)

		link := getLogPath(symlinkDir, pod.Name, pod.Namespace, "c1", "ready-uid")
		_, err := os.Lstat(link)
		assert.NoError(t, err, "UpdateFunc should create the symlink")
	})

	t.Run("DeleteFunc is a no-op", func(t *testing.T) {
		symlinkDir, _ := redirectLogPaths(t)
		handler.OnDelete(readyPod())
		entries, err := os.ReadDir(symlinkDir)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}
