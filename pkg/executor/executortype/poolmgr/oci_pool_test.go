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

package poolmgr

import (
	"sort"
	"strings"
	"testing"

	"github.com/gkampitakis/go-snaps/snaps"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigsyaml "sigs.k8s.io/yaml"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fetcherConfig "github.com/fission/fission/pkg/fetcher/config"
)

func newOCITestEnv() *fv1.Environment {
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

func newOCITestFn() *fv1.Function {
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
					ExecutorType: fv1.ExecutorTypePoolmgr,
					MinScale:     2,
				},
			},
		},
	}
}

func newOCITestArchive() *fv1.OCIArchive {
	return &fv1.OCIArchive{
		Image: "ghcr.io/example/hello:v1",
		ImagePullSecrets: []apiv1.LocalObjectReference{
			{Name: "ghcr-pull"},
		},
	}
}

// TestBuildOCIPoolDeployment_Snapshot locks down the OCI poolmgr pod
// shape: image volume mounted RO at /userfunc, fetcher sidecar with
// -skip-fetch, no userfunc emptyDir, env-level + OCI pull secrets merged.
func TestBuildOCIPoolDeployment_Snapshot(t *testing.T) {
	cfg, err := fetcherConfig.MakeFetcherConfig("/userfunc")
	if err != nil {
		t.Fatalf("fetcher config: %v", err)
	}
	dep, err := buildOCIPoolDeployment(
		newOCITestFn(),
		newOCITestEnv(),
		newOCITestArchive(),
		cfg,
		apiv1.PullIfNotPresent,
		nil,   // podSpecPatch
		false, // useIstio
		"oci-hello-default-aabbccdd",
		map[string]string{"app": "oci-hello", "executorType": "poolmgr"},
		map[string]string{"executorInstanceId": "test"},
		false, // enableOwnerReferences
	)
	if err != nil {
		t.Fatalf("buildOCIPoolDeployment: %v", err)
	}

	containers := dep.Spec.Template.Spec.Containers
	names := containerNames(containers)
	if !contains(names, "node") || !contains(names, "fetcher") {
		t.Fatalf("expected node and fetcher containers, got %v", names)
	}

	var fetcher *apiv1.Container
	for i := range containers {
		if containers[i].Name == "fetcher" {
			fetcher = &containers[i]
			break
		}
	}
	if fetcher == nil {
		t.Fatalf("fetcher container missing")
	}
	if !commandContains(fetcher.Command, "-skip-fetch") {
		t.Fatalf("fetcher command must include -skip-fetch, got %v", fetcher.Command)
	}
	if !commandContains(fetcher.Command, "-specialize-on-startup") {
		t.Fatalf("fetcher command must include -specialize-on-startup, got %v", fetcher.Command)
	}

	// Image volume: name == userfunc, sourced from OCIArchive.Image,
	// no emptyDir for userfunc.
	var userfuncVol *apiv1.Volume
	for i := range dep.Spec.Template.Spec.Volumes {
		v := dep.Spec.Template.Spec.Volumes[i]
		if v.Name == fv1.SharedVolumeUserfunc {
			userfuncVol = &v
		}
	}
	if userfuncVol == nil {
		t.Fatalf("userfunc volume missing")
	}
	if userfuncVol.EmptyDir != nil {
		t.Fatalf("userfunc volume must not be EmptyDir in OCI mode")
	}
	if userfuncVol.Image == nil || userfuncVol.Image.Reference != "ghcr.io/example/hello:v1" {
		t.Fatalf("userfunc volume must reference OCI image, got %+v", userfuncVol.Image)
	}

	// Pull secrets: env's "env-pull" + OCI's "ghcr-pull"
	wantSecrets := map[string]bool{"env-pull": false, "ghcr-pull": false}
	for _, s := range dep.Spec.Template.Spec.ImagePullSecrets {
		if _, ok := wantSecrets[s.Name]; ok {
			wantSecrets[s.Name] = true
		}
	}
	for name, found := range wantSecrets {
		if !found {
			t.Fatalf("expected ImagePullSecret %q on pod spec, got %+v", name, dep.Spec.Template.Spec.ImagePullSecrets)
		}
	}

	// Snapshot: stabilize ImagePullSecret order before marshal.
	specCopy := dep.Spec.Template.Spec
	sort.Slice(specCopy.ImagePullSecrets, func(i, j int) bool {
		return specCopy.ImagePullSecrets[i].Name < specCopy.ImagePullSecrets[j].Name
	})
	out, err := sigsyaml.Marshal(specCopy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	snaps.MatchSnapshot(t, string(out))
}

// TestBuildOCIPoolDeployment_RejectsEmptyImage asserts the builder
// refuses to produce a pod spec when the OCIArchive is missing or empty.
// This guards against silently shipping a pod that would fail to pull.
func TestBuildOCIPoolDeployment_RejectsEmptyImage(t *testing.T) {
	cfg, _ := fetcherConfig.MakeFetcherConfig("/userfunc")
	for _, tc := range []struct {
		name string
		oci  *fv1.OCIArchive
	}{
		{name: "nil archive", oci: nil},
		{name: "empty image", oci: &fv1.OCIArchive{Image: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildOCIPoolDeployment(newOCITestFn(), newOCITestEnv(), tc.oci, cfg,
				apiv1.PullIfNotPresent, nil, false, "x", nil, nil, false)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}

// TestBuildOCIPoolDeployment_ReplicasFromMinScale verifies the function's
// MinScale flows into the deployment's replica count, with a default
// applied when MinScale is zero/unset.
func TestBuildOCIPoolDeployment_ReplicasFromMinScale(t *testing.T) {
	cfg, _ := fetcherConfig.MakeFetcherConfig("/userfunc")
	for _, tc := range []struct {
		name     string
		minScale int
		want     int32
	}{
		{name: "zero falls back to default", minScale: 0, want: ociPoolDefaultReplicas},
		{name: "explicit min wins", minScale: 5, want: 5},
		{name: "min of one", minScale: 1, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fn := newOCITestFn()
			fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale = tc.minScale
			dep, err := buildOCIPoolDeployment(fn, newOCITestEnv(), newOCITestArchive(), cfg,
				apiv1.PullIfNotPresent, nil, false, "x", nil, nil, false)
			if err != nil {
				t.Fatalf("buildOCIPoolDeployment: %v", err)
			}
			if dep.Spec.Replicas == nil || *dep.Spec.Replicas != tc.want {
				t.Fatalf("replicas = %v, want %d", dep.Spec.Replicas, tc.want)
			}
		})
	}
}

// TestOCIFunctionObjName sanity-checks the deterministic name builder.
func TestOCIFunctionObjName(t *testing.T) {
	fn := newOCITestFn()
	name := ociFunctionObjName(fn)
	if !strings.HasPrefix(name, "oci-") {
		t.Fatalf("name should start with oci-: %q", name)
	}
	if len(name) > 63 {
		t.Fatalf("name too long for k8s (%d): %q", len(name), name)
	}
	// Should be deterministic across calls.
	if again := ociFunctionObjName(fn); again != name {
		t.Fatalf("name not deterministic: %q vs %q", name, again)
	}
}

func containerNames(cs []apiv1.Container) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func commandContains(cmd []string, target string) bool {
	for _, c := range cmd {
		if c == target {
			return true
		}
	}
	return false
}

// TestIsOCIPoolFsvc covers the discriminator the reaper uses to decide
// which fsvcs are OCI-flavored. Tarball pool fsvcs ship pod refs; OCI
// pool fsvcs ship a deployment ref.
func TestIsOCIPoolFsvc(t *testing.T) {
	for _, tc := range []struct {
		name string
		objs []apiv1.ObjectReference
		want bool
	}{
		{name: "tarball pool (pod only)", objs: []apiv1.ObjectReference{{Kind: "pod", Name: "p1"}}, want: false},
		{name: "OCI pool", objs: []apiv1.ObjectReference{{Kind: "deployment", Name: "d1"}, {Kind: "service", Name: "s1"}}, want: true},
		{name: "kind is case-insensitive", objs: []apiv1.ObjectReference{{Kind: "Deployment", Name: "d1"}}, want: true},
		{name: "empty list", objs: nil, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isOCIPoolFsvc(tc.objs); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOCIPoolImage extracts the image-volume reference from a Deployment
// — the helper the drift detector uses to tell stale pools from current.
func TestOCIPoolImage(t *testing.T) {
	cfg, _ := fetcherConfig.MakeFetcherConfig("/userfunc")
	dep, err := buildOCIPoolDeployment(newOCITestFn(), newOCITestEnv(), newOCITestArchive(), cfg,
		apiv1.PullIfNotPresent, nil, false, "x", nil, nil, false)
	if err != nil {
		t.Fatalf("buildOCIPoolDeployment: %v", err)
	}
	if got := ociPoolImage(dep); got != "ghcr.io/example/hello:v1" {
		t.Fatalf("ociPoolImage = %q, want %q", got, "ghcr.io/example/hello:v1")
	}

	// Deployment without image volume returns empty string.
	bare := dep.DeepCopy()
	for i := range bare.Spec.Template.Spec.Volumes {
		bare.Spec.Template.Spec.Volumes[i].Image = nil
	}
	if got := ociPoolImage(bare); got != "" {
		t.Fatalf("non-OCI deployment must return empty image, got %q", got)
	}
}

// TestMinScaleAlwaysWarm covers the always-warm contract: any explicit
// MinScale > 0 takes the function out of the idle-reap candidate set.
func TestMinScaleAlwaysWarm(t *testing.T) {
	for _, tc := range []struct {
		name     string
		minScale int
		want     bool
	}{
		{name: "zero is reapable", minScale: 0, want: false},
		{name: "one is always-warm", minScale: 1, want: true},
		{name: "five is always-warm", minScale: 5, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fn := newOCITestFn()
			fn.Spec.InvokeStrategy.ExecutionStrategy.MinScale = tc.minScale
			if got := minScaleAlwaysWarm(fn); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
