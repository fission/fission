// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package _package

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	fissionfake "github.com/fission/fission/pkg/generated/clientset/versioned/fake"
)

const (
	watchTestNS  = "default"
	watchTestPkg = "watch-pkg"
	watchTestEnv = "watch-env"
)

func watchTestPackage(status fv1.BuildStatus) *fv1.Package {
	return &fv1.Package{
		Name: watchTestPkg, Namespace: watchTestNS,
		Spec: fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{Name: watchTestEnv, Namespace: watchTestNS},
		},
		Status: fv1.PackageStatus{BuildStatus: status},
	}
}

func watchTestBuilderPod() *corev1.Pod {
	return &corev1.Pod{
		Name:      watchTestEnv + "-1234-abc",
		Namespace: watchTestNS,
		Labels: map[string]string{
			fv1.BuilderLabelOwner:   fv1.BuilderOwnerBuilderMgr,
			fv1.BuilderLabelEnvName: watchTestEnv,
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: fv1.BuilderContainerName}}},
		Status: corev1.PodStatus{
			PodIP:             "10.1.2.3",
			ContainerStatuses: []corev1.ContainerStatus{{Name: fv1.BuilderContainerName, Ready: true}},
		},
	}
}

// setBuildStatus flips the package's build status (and optionally the build
// log) through the fake clientset's status subresource, the way buildermgr
// does. It is called from spawned goroutines, so it must not use require
// (t.FailNow is only legal on the test goroutine); assert's t.Errorf is safe
// and still fails the test with the real error.
func setBuildStatus(t *testing.T, client cmd.Client, status fv1.BuildStatus, buildLog string) {
	t.Helper()
	pkg, err := client.FissionClientSet.CoreV1().Packages(watchTestNS).Get(t.Context(), watchTestPkg, metav1.GetOptions{})
	if !assert.NoError(t, err) {
		return
	}
	pkg.Status.BuildStatus = status
	pkg.Status.BuildLog = buildLog
	_, err = client.FissionClientSet.CoreV1().Packages(watchTestNS).UpdateStatus(t.Context(), pkg, metav1.UpdateOptions{})
	assert.NoError(t, err)
}

func TestWatchPackageBuildSucceeds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := cmd.Client{
			FissionClientSet: fissionfake.NewSimpleClientset(watchTestPackage(fv1.BuildStatusPending)),
			KubernetesClient: k8sfake.NewSimpleClientset(watchTestBuilderPod()),
		}

		go func() {
			time.Sleep(2 * time.Second)
			setBuildStatus(t, client, fv1.BuildStatusRunning, "")
			time.Sleep(2 * time.Second)
			setBuildStatus(t, client, fv1.BuildStatusSucceeded, "collecting deps\nbuild ok\n")
		}()

		var out bytes.Buffer
		err := watchPackageBuild(t.Context(), client, &out, watchTestNS, watchTestPkg)
		require.NoError(t, err)

		got := out.String()
		assert.Contains(t, got, "Waiting for package 'watch-pkg' build to start")
		assert.Contains(t, got, "Package 'watch-pkg' build running")
		// The live stream is plumbed through the fake pod-log endpoint, whose
		// canned body is "fake logs" — content fidelity is integration-tested.
		assert.Contains(t, got, "fake logs")
		// The final report is the authoritative Status.BuildLog.
		assert.Contains(t, got, "========= build log =========")
		assert.Contains(t, got, "collecting deps\nbuild ok\n")
		assert.Contains(t, got, "Package 'watch-pkg' build succeeded")
	})
}

func TestWatchPackageBuildFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := cmd.Client{
			FissionClientSet: fissionfake.NewSimpleClientset(watchTestPackage(fv1.BuildStatusPending)),
			KubernetesClient: k8sfake.NewSimpleClientset(), // no builder pod at all: stream must degrade silently
		}

		go func() {
			time.Sleep(2 * time.Second)
			setBuildStatus(t, client, fv1.BuildStatusFailed, "error: no module named flask\n")
		}()

		var out bytes.Buffer
		err := watchPackageBuild(t.Context(), client, &out, watchTestNS, watchTestPkg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "build failed")

		got := out.String()
		assert.Contains(t, got, "error: no module named flask")
	})
}

func TestWatchPackageBuildNothingToBuild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := cmd.Client{
			FissionClientSet: fissionfake.NewSimpleClientset(watchTestPackage(fv1.BuildStatusNone)),
			KubernetesClient: k8sfake.NewSimpleClientset(),
		}

		var out bytes.Buffer
		err := watchPackageBuild(t.Context(), client, &out, watchTestNS, watchTestPkg)
		require.NoError(t, err)
		assert.Contains(t, out.String(), "has nothing to build")
		assert.NotContains(t, out.String(), "========= build log =========")
	})
}

func TestWatchPackageBuildTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := cmd.Client{
			FissionClientSet: fissionfake.NewSimpleClientset(watchTestPackage(fv1.BuildStatusPending)),
			KubernetesClient: k8sfake.NewSimpleClientset(),
		}

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		var out bytes.Buffer
		err := watchPackageBuild(ctx, client, &out, watchTestNS, watchTestPkg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timed out")
	})
}

func TestWatchPackageBuildPackageDeleted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := cmd.Client{
			FissionClientSet: fissionfake.NewSimpleClientset(watchTestPackage(fv1.BuildStatusPending)),
			KubernetesClient: k8sfake.NewSimpleClientset(),
		}

		go func() {
			time.Sleep(2 * time.Second)
			err := client.FissionClientSet.CoreV1().Packages(watchTestNS).Delete(t.Context(), watchTestPkg, metav1.DeleteOptions{})
			assert.NoError(t, err) // not require: FailNow is illegal off the test goroutine
		}()

		var out bytes.Buffer
		err := watchPackageBuild(t.Context(), client, &out, watchTestNS, watchTestPkg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no longer exists")
	})
}

func TestWatchPackageBuildSurfacesPersistentAPIErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fc := fissionfake.NewSimpleClientset(watchTestPackage(fv1.BuildStatusPending))
		fc.PrependReactor("get", "packages", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("the server has asked for the client to provide credentials")
		})
		client := cmd.Client{FissionClientSet: fc, KubernetesClient: k8sfake.NewSimpleClientset()}

		var out bytes.Buffer
		err := watchPackageBuild(t.Context(), client, &out, watchTestNS, watchTestPkg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "repeatedly failed")
		assert.Contains(t, err.Error(), "provide credentials")
	})
}

func TestWatchPackageBuildRefusesBuilderlessEnv(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pkg := watchTestPackage(fv1.BuildStatusPending)
		pkg.Spec.Source = fv1.Archive{Type: fv1.ArchiveTypeUrl, URL: "http://storage/src.zip"}
		env := &fv1.Environment{
			Name: watchTestEnv, Namespace: watchTestNS,
			// No Builder.Image: this env can never run a build.
		}
		client := cmd.Client{
			FissionClientSet: fissionfake.NewSimpleClientset(pkg, env),
			KubernetesClient: k8sfake.NewSimpleClientset(),
		}

		var out bytes.Buffer
		err := watchPackageBuild(t.Context(), client, &out, watchTestNS, watchTestPkg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no builder")
	})
}

func TestPickBuilderPod(t *testing.T) {
	t.Parallel()

	mkPod := func(name, rv string, ready bool, age time.Duration) corev1.Pod {
		return corev1.Pod{
			Name:              name,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
			Labels: map[string]string{
				fv1.BuilderLabelOwner:              fv1.BuilderOwnerBuilderMgr,
				fv1.BuilderLabelEnvName:            watchTestEnv,
				fv1.BuilderLabelEnvResourceVersion: rv,
			},
			Status: corev1.PodStatus{
				PodIP:             "10.1.2.3",
				ContainerStatuses: []corev1.ContainerStatus{{Ready: ready}},
			},
		}
	}

	tests := []struct {
		name  string
		pods  []corev1.Pod
		envRV string
		want  string // expected pod name, "" for nil
	}{
		{name: "empty list", pods: nil, envRV: "5", want: ""},
		{
			name: "ready beats newer non-ready",
			pods: []corev1.Pod{
				mkPod("young-notready", "5", false, time.Minute),
				mkPod("old-ready", "5", true, time.Hour),
			},
			envRV: "5",
			want:  "old-ready",
		},
		{
			name: "current env generation beats stale",
			pods: []corev1.Pod{
				mkPod("stale-rv", "4", true, time.Hour),
				mkPod("current-rv", "5", true, 2*time.Hour),
			},
			envRV: "5",
			want:  "current-rv",
		},
		{
			name: "newest wins on equal rank",
			pods: []corev1.Pod{
				mkPod("older", "5", true, time.Hour),
				mkPod("newer", "5", true, time.Minute),
			},
			envRV: "5",
			want:  "newer",
		},
		{
			name: "unknown env RV falls back to newest ready",
			pods: []corev1.Pod{
				mkPod("older", "4", true, time.Hour),
				mkPod("newer", "6", true, time.Minute),
			},
			envRV: "",
			want:  "newer",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := pickBuilderPod(tc.pods, tc.envRV)
			if tc.want == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tc.want, got.Name)
		})
	}
}
