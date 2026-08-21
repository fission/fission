// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package newdeploy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// TestPodTemplateChanged pins the diff updateFunction uses to decide whether to
// roll the deployment. A field that the deployment builder bakes into the pod
// template but that is missing here leaves the CR and the running container
// disagreeing indefinitely — the RFC-0030 env fields were exactly that bug.
func TestPodTemplateChanged(t *testing.T) {
	t.Parallel()

	base := func() *fv1.Function {
		return &fv1.Function{Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{Namespace: "ns", Name: "env"},
			Package:     fv1.FunctionPackageRef{PackageRef: fv1.PackageRef{Namespace: "ns", Name: "pkg"}},
			Secrets:     []fv1.SecretReference{{Namespace: "ns", Name: "s1"}},
			ConfigMaps:  []fv1.ConfigMapReference{{Namespace: "ns", Name: "c1"}},
			Env:         []apiv1.EnvVar{{Name: "DATABASE_URL", Value: "postgres://old"}},
			EnvFrom: []apiv1.EnvFromSource{{
				SecretRef: &apiv1.SecretEnvSource{Name: "creds"},
			}},
		}}
	}

	cases := []struct {
		name   string
		mutate func(*fv1.Function)
		want   bool
	}{
		{"unchanged", func(*fv1.Function) {}, false},
		{"environment", func(f *fv1.Function) { f.Spec.Environment.Name = "env2" }, true},
		{"package", func(f *fv1.Function) { f.Spec.Package.PackageRef.Name = "pkg2" }, true},
		{"entrypoint", func(f *fv1.Function) { f.Spec.Package.FunctionName = "other" }, true},
		{"secrets", func(f *fv1.Function) { f.Spec.Secrets = nil }, true},
		{"configmaps", func(f *fv1.Function) { f.Spec.ConfigMaps = nil }, true},
		{"env value", func(f *fv1.Function) { f.Spec.Env[0].Value = "postgres://new" }, true},
		{"env added", func(f *fv1.Function) {
			f.Spec.Env = append(f.Spec.Env, apiv1.EnvVar{Name: "LOG_LEVEL", Value: "debug"})
		}, true},
		{"envFrom removed", func(f *fv1.Function) { f.Spec.EnvFrom = nil }, true},
		{"idle timeout is not a pod-template field", func(f *fv1.Function) {
			idle := 42
			f.Spec.IdleTimeout = &idle
		}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			oldFn, newFn := base(), base()
			tc.mutate(newFn)
			assert.Equal(t, tc.want, podTemplateChanged(oldFn, newFn))
		})
	}
}
