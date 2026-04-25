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

package newdeploy

import (
	"sort"
	"testing"

	"github.com/gkampitakis/go-snaps/snaps"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	sigsyaml "sigs.k8s.io/yaml"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// stableDeploymentTestEnv returns a deterministic Environment for snapshot
// tests. Anything time- or UID-dependent is held constant so the snapshot
// stays stable across runs.
func stableDeploymentTestEnv() *fv1.Environment {
	return &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node",
			Namespace: "default",
			UID:       "11111111-1111-1111-1111-111111111111",
		},
		Spec: fv1.EnvironmentSpec{
			Version: 2,
			Runtime: fv1.Runtime{
				Image: "ghcr.io/fission/node-env-22",
			},
			ImagePullSecret: "env-pull",
		},
	}
}

func stableDeploymentTestFunction() *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hello",
			Namespace: "default",
			UID:       "22222222-2222-2222-2222-222222222222",
		},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{
				Name:      "node",
				Namespace: "default",
			},
			Package: fv1.FunctionPackageRef{
				PackageRef: fv1.PackageRef{
					Name:            "hello-pkg",
					Namespace:       "default",
					ResourceVersion: "100",
				},
				FunctionName: "handler",
			},
			InvokeStrategy: fv1.InvokeStrategy{
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType: fv1.ExecutorTypeNewdeploy,
					MinScale:     1,
					MaxScale:     3,
				},
			},
		},
	}
}

func newTestDeploy(t *testing.T) *NewDeploy {
	t.Helper()
	cfg, err := fetcherConfig.MakeFetcherConfig("/userfunc")
	if err != nil {
		t.Fatalf("fetcher config: %v", err)
	}
	return &NewDeploy{
		logger:                 loggerfactory.GetLogger(),
		kubernetesClient:       fake.NewClientset(),
		instanceID:             "test-instance",
		fetcherConfig:          cfg,
		runtimeImagePullPolicy: apiv1.PullIfNotPresent,
		enableOwnerReferences:  false,
	}
}

func sortPodSecurity(spec *apiv1.PodSpec) {
	sort.Slice(spec.ImagePullSecrets, func(i, j int) bool {
		return spec.ImagePullSecrets[i].Name < spec.ImagePullSecrets[j].Name
	})
}

// TestGetDeploymentSpecTarballPath captures the legacy fetcher-based
// pod spec to guard against regressions from the OCI branch.
func TestGetDeploymentSpecTarballPath(t *testing.T) {
	deploy := newTestDeploy(t)
	env := stableDeploymentTestEnv()
	fn := stableDeploymentTestFunction()
	replicas := int32(1)

	dep, err := deploy.getDeploymentSpec(t.Context(), fn, env, nil, &replicas,
		"newdeploy-hello-default-aabbccdd", "fission-function",
		map[string]string{"app": "hello"},
		map[string]string{"executorInstanceId": "test-instance"})
	if err != nil {
		t.Fatalf("getDeploymentSpec: %v", err)
	}

	// Sanity assertions: fetcher container present, image is env runtime.
	containerNames := containerNamesOf(dep.Spec.Template.Spec.Containers)
	if !containsString(containerNames, "fetcher") {
		t.Fatalf("tarball path must include fetcher container, got %v", containerNames)
	}
	mainImage := imageForContainer(t, dep.Spec.Template.Spec.Containers, env.Name)
	if mainImage != env.Spec.Runtime.Image {
		t.Fatalf("main container image = %q, want %q", mainImage, env.Spec.Runtime.Image)
	}

	out, err := sigsyaml.Marshal(dep.Spec.Template.Spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	snaps.MatchSnapshot(t, string(out))
}

// TestGetDeploymentSpecOCIPath verifies that with an OCIArchive on the
// Package, the resulting pod spec uses the OCI image, has no fetcher
// sidecar, and merges pull secrets correctly.
func TestGetDeploymentSpecOCIPath(t *testing.T) {
	deploy := newTestDeploy(t)
	env := stableDeploymentTestEnv()
	fn := stableDeploymentTestFunction()
	replicas := int32(1)

	oci := &fv1.OCIArchive{
		Image: "ghcr.io/example/hello:v1",
		ImagePullSecrets: []apiv1.LocalObjectReference{
			{Name: "ghcr-pull"},
			{Name: "env-pull"}, // duplicate of env to exercise dedupe
		},
	}

	dep, err := deploy.getDeploymentSpec(t.Context(), fn, env, oci, &replicas,
		"newdeploy-hello-default-aabbccdd", "fission-function",
		map[string]string{"app": "hello"},
		map[string]string{"executorInstanceId": "test-instance"})
	if err != nil {
		t.Fatalf("getDeploymentSpec: %v", err)
	}

	containers := dep.Spec.Template.Spec.Containers
	if got := len(containers); got != 1 {
		t.Fatalf("expected exactly 1 container in OCI mode, got %d (%v)", got, containerNamesOf(containers))
	}
	if containers[0].Image != oci.Image {
		t.Fatalf("main container image = %q, want %q", containers[0].Image, oci.Image)
	}
	if len(dep.Spec.Template.Spec.Volumes) != 0 {
		t.Fatalf("OCI mode must not add fetcher volumes, got %+v", dep.Spec.Template.Spec.Volumes)
	}
	wantSecrets := []apiv1.LocalObjectReference{
		{Name: "env-pull"},
		{Name: "ghcr-pull"},
	}
	got := append([]apiv1.LocalObjectReference(nil), dep.Spec.Template.Spec.ImagePullSecrets...)
	sort.Slice(got, func(i, j int) bool { return got[i].Name < got[j].Name })
	if !equalSecrets(got, wantSecrets) {
		t.Fatalf("ImagePullSecrets = %+v, want (sorted) %+v", got, wantSecrets)
	}

	// Snapshot the pod spec for stability — sort pull secrets first
	// because MergeImagePullSecrets ordering depends on input order
	// which is fixed but a sort makes the snapshot trivially diffable.
	specCopy := dep.Spec.Template.Spec
	sortPodSecurity(&specCopy)
	out, err := sigsyaml.Marshal(specCopy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	snaps.MatchSnapshot(t, string(out))
}

func containerNamesOf(cs []apiv1.Container) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func imageForContainer(t *testing.T, cs []apiv1.Container, name string) string {
	t.Helper()
	for _, c := range cs {
		if c.Name == name {
			return c.Image
		}
	}
	t.Fatalf("container %q not found", name)
	return ""
}

func equalSecrets(a, b []apiv1.LocalObjectReference) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}
