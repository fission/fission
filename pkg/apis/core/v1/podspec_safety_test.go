// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"reflect"
	"strings"
	"testing"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidatePodSpecSafety_Nil(t *testing.T) {
	if err := ValidatePodSpecSafety("Function.spec.podspec", nil); err != nil {
		t.Fatalf("nil podspec must be accepted, got: %v", err)
	}
}

func TestValidatePodSpecSafety_Benign(t *testing.T) {
	allow := false
	ps := &apiv1.PodSpec{
		Containers: []apiv1.Container{{
			Name:    "user",
			Image:   "alpine:3.19",
			Command: []string{"/bin/sh", "-c", "echo hi"},
			Env:     []apiv1.EnvVar{{Name: "FOO", Value: "bar"}},
			SecurityContext: &apiv1.SecurityContext{
				AllowPrivilegeEscalation: &allow,
				Capabilities: &apiv1.Capabilities{
					Add: []apiv1.Capability{"NET_BIND_SERVICE"},
				},
			},
		}},
		Volumes: []apiv1.Volume{{
			Name: "cm",
			VolumeSource: apiv1.VolumeSource{
				ConfigMap: &apiv1.ConfigMapVolumeSource{
					LocalObjectReference: apiv1.LocalObjectReference{Name: "my-cm"},
				},
			},
		}},
		NodeSelector: map[string]string{"role": "fn"},
	}
	if err := ValidatePodSpecSafety("Function.spec.podspec", ps); err != nil {
		t.Fatalf("benign podspec must be accepted, got: %v", err)
	}
}

func TestValidatePodSpecSafety_DangerousFields(t *testing.T) {
	on := true
	cases := []struct {
		name      string
		mutate    func(*apiv1.PodSpec)
		wantInErr string
	}{
		{
			name:      "hostNetwork",
			mutate:    func(ps *apiv1.PodSpec) { ps.HostNetwork = true },
			wantInErr: "hostNetwork",
		},
		{
			name:      "hostPID",
			mutate:    func(ps *apiv1.PodSpec) { ps.HostPID = true },
			wantInErr: "hostPID",
		},
		{
			name:      "hostIPC",
			mutate:    func(ps *apiv1.PodSpec) { ps.HostIPC = true },
			wantInErr: "hostIPC",
		},
		{
			name:      "serviceAccountName override",
			mutate:    func(ps *apiv1.PodSpec) { ps.ServiceAccountName = "cluster-admin" },
			wantInErr: "serviceAccountName",
		},
		{
			name:      "deprecated serviceAccount (alias) override",
			mutate:    func(ps *apiv1.PodSpec) { ps.DeprecatedServiceAccount = "cluster-admin" },
			wantInErr: "serviceAccount",
		},
		{
			name: "hostPath volume",
			mutate: func(ps *apiv1.PodSpec) {
				ps.Volumes = []apiv1.Volume{{
					Name: "host-root",
					VolumeSource: apiv1.VolumeSource{
						HostPath: &apiv1.HostPathVolumeSource{Path: "/"},
					},
				}}
			},
			wantInErr: "hostPath",
		},
		{
			name: "privileged container",
			mutate: func(ps *apiv1.PodSpec) {
				ps.Containers = []apiv1.Container{{
					Name:            "user",
					SecurityContext: &apiv1.SecurityContext{Privileged: &on},
				}}
			},
			wantInErr: "privileged",
		},
		{
			name: "allowPrivilegeEscalation=true",
			mutate: func(ps *apiv1.PodSpec) {
				ps.Containers = []apiv1.Container{{
					Name:            "user",
					SecurityContext: &apiv1.SecurityContext{AllowPrivilegeEscalation: &on},
				}}
			},
			wantInErr: "allowPrivilegeEscalation",
		},
		{
			name: "SYS_ADMIN capability",
			mutate: func(ps *apiv1.PodSpec) {
				ps.Containers = []apiv1.Container{{
					Name: "user",
					SecurityContext: &apiv1.SecurityContext{
						Capabilities: &apiv1.Capabilities{
							Add: []apiv1.Capability{"SYS_ADMIN"},
						},
					},
				}}
			},
			wantInErr: "SYS_ADMIN",
		},
		{
			name: "NET_ADMIN capability",
			mutate: func(ps *apiv1.PodSpec) {
				ps.Containers = []apiv1.Container{{
					Name: "user",
					SecurityContext: &apiv1.SecurityContext{
						Capabilities: &apiv1.Capabilities{
							Add: []apiv1.Capability{"NET_ADMIN"},
						},
					},
				}}
			},
			wantInErr: "NET_ADMIN",
		},
		// GHSA-qf5v-m7p4-95rp regression coverage: the prior denylist omitted
		// these escape-class capabilities. The allowlist rejects all of them.
		{
			name: "SYS_TIME capability (node clock corruption)",
			mutate: func(ps *apiv1.PodSpec) {
				ps.Containers = []apiv1.Container{{
					Name: "user",
					SecurityContext: &apiv1.SecurityContext{
						Capabilities: &apiv1.Capabilities{Add: []apiv1.Capability{"SYS_TIME"}},
					},
				}}
			},
			wantInErr: "SYS_TIME",
		},
		{
			name: "SYS_RAWIO capability",
			mutate: func(ps *apiv1.PodSpec) {
				ps.Containers = []apiv1.Container{{
					Name: "user",
					SecurityContext: &apiv1.SecurityContext{
						Capabilities: &apiv1.Capabilities{Add: []apiv1.Capability{"SYS_RAWIO"}},
					},
				}}
			},
			wantInErr: "SYS_RAWIO",
		},
		{
			name: "BPF capability",
			mutate: func(ps *apiv1.PodSpec) {
				ps.Containers = []apiv1.Container{{
					Name: "user",
					SecurityContext: &apiv1.SecurityContext{
						Capabilities: &apiv1.Capabilities{Add: []apiv1.Capability{"BPF"}},
					},
				}}
			},
			wantInErr: "BPF",
		},
		{
			name: "SYS_RESOURCE capability",
			mutate: func(ps *apiv1.PodSpec) {
				ps.Containers = []apiv1.Container{{
					Name: "user",
					SecurityContext: &apiv1.SecurityContext{
						Capabilities: &apiv1.Capabilities{Add: []apiv1.Capability{"SYS_RESOURCE"}},
					},
				}}
			},
			wantInErr: "SYS_RESOURCE",
		},
		{
			name: "MAC_ADMIN capability",
			mutate: func(ps *apiv1.PodSpec) {
				ps.Containers = []apiv1.Container{{
					Name: "user",
					SecurityContext: &apiv1.SecurityContext{
						Capabilities: &apiv1.Capabilities{Add: []apiv1.Capability{"MAC_ADMIN"}},
					},
				}}
			},
			wantInErr: "MAC_ADMIN",
		},
		// Allowlist also rejects benign-by-old-standards but not-in-allowlist caps
		// (the prior denylist let these through, the allowlist does not).
		{
			name: "CHOWN capability (rejected under allowlist)",
			mutate: func(ps *apiv1.PodSpec) {
				ps.Containers = []apiv1.Container{{
					Name: "user",
					SecurityContext: &apiv1.SecurityContext{
						Capabilities: &apiv1.Capabilities{Add: []apiv1.Capability{"CHOWN"}},
					},
				}}
			},
			wantInErr: "CHOWN",
		},
		{
			name: "privileged init container",
			mutate: func(ps *apiv1.PodSpec) {
				ps.InitContainers = []apiv1.Container{{
					Name:            "init",
					SecurityContext: &apiv1.SecurityContext{Privileged: &on},
				}}
			},
			wantInErr: "initContainers",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps := &apiv1.PodSpec{Containers: []apiv1.Container{{Name: "user"}}}
			tc.mutate(ps)
			err := ValidatePodSpecSafety("Function.spec.podspec", ps)
			if err == nil {
				t.Fatalf("expected rejection for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("error must mention %q, got: %v", tc.wantInErr, err)
			}
			if !strings.Contains(err.Error(), "Function.spec.podspec") {
				t.Fatalf("error must include the field prefix, got: %v", err)
			}
		})
	}
}

// TestValidateContainerSafety covers the standalone-container check used for
// Environment Runtime.Container / Builder.Container. Closes GHSA-m63v-2g9w-2w6v.
func TestValidateContainerSafety(t *testing.T) {
	on := true
	off := false

	t.Run("nil container is accepted", func(t *testing.T) {
		if err := ValidateContainerSafety("Environment.spec.runtime.container", nil); err != nil {
			t.Fatalf("nil container must be accepted, got: %v", err)
		}
	})

	t.Run("nil securityContext is accepted", func(t *testing.T) {
		c := &apiv1.Container{Name: "py", Image: "fission/python-env:latest"}
		if err := ValidateContainerSafety("Environment.spec.runtime.container", c); err != nil {
			t.Fatalf("container without securityContext must be accepted, got: %v", err)
		}
	})

	t.Run("benign securityContext is accepted", func(t *testing.T) {
		c := &apiv1.Container{
			Name: "py",
			SecurityContext: &apiv1.SecurityContext{
				AllowPrivilegeEscalation: &off,
				Capabilities:             &apiv1.Capabilities{Add: []apiv1.Capability{"NET_BIND_SERVICE"}},
			},
		}
		if err := ValidateContainerSafety("Environment.spec.runtime.container", c); err != nil {
			t.Fatalf("benign container must be accepted, got: %v", err)
		}
	})

	cases := []struct {
		name      string
		sc        *apiv1.SecurityContext
		wantInErr string
	}{
		{"privileged", &apiv1.SecurityContext{Privileged: &on}, "privileged"},
		{"allowPrivilegeEscalation", &apiv1.SecurityContext{AllowPrivilegeEscalation: &on}, "allowPrivilegeEscalation"},
		{"SYS_ADMIN", &apiv1.SecurityContext{Capabilities: &apiv1.Capabilities{Add: []apiv1.Capability{"SYS_ADMIN"}}}, "SYS_ADMIN"},
		{"NET_ADMIN", &apiv1.SecurityContext{Capabilities: &apiv1.Capabilities{Add: []apiv1.Capability{"NET_ADMIN"}}}, "NET_ADMIN"},
		// GHSA-qf5v: allowlist must reject SYS_TIME and CHOWN that the denylist let through.
		{"SYS_TIME", &apiv1.SecurityContext{Capabilities: &apiv1.Capabilities{Add: []apiv1.Capability{"SYS_TIME"}}}, "SYS_TIME"},
		{"CHOWN", &apiv1.SecurityContext{Capabilities: &apiv1.Capabilities{Add: []apiv1.Capability{"CHOWN"}}}, "CHOWN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &apiv1.Container{Name: "py", SecurityContext: tc.sc}
			err := ValidateContainerSafety("Environment.spec.runtime.container", c)
			if err == nil {
				t.Fatalf("expected rejection for %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("error must mention %q, got: %v", tc.wantInErr, err)
			}
			if !strings.Contains(err.Error(), "Environment.spec.runtime.container") {
				t.Fatalf("error must include the field prefix, got: %v", err)
			}
		})
	}
}

// TestValidatePodSpecSafety_AllowedCapability asserts that NET_BIND_SERVICE
// — the sole entry on the PSA-restricted allowlist — flows through. The
// allowlist is intentionally narrow so legitimate function workloads can
// still bind to privileged ports, and every other capability (including the
// OCI defaults like CHOWN/MKNOD that the prior denylist accepted) is rejected.
func TestValidatePodSpecSafety_AllowedCapability(t *testing.T) {
	ps := &apiv1.PodSpec{
		Containers: []apiv1.Container{{
			Name: "user",
			SecurityContext: &apiv1.SecurityContext{
				Capabilities: &apiv1.Capabilities{
					Add: []apiv1.Capability{"NET_BIND_SERVICE"},
				},
			},
		}},
	}
	if err := ValidatePodSpecSafety("Function.spec.podspec", ps); err != nil {
		t.Fatalf("NET_BIND_SERVICE must flow through, got: %v", err)
	}
}

// TestAllTenantContainerSurfacesAreValidated is the forward-compat regression
// guard for the PodSpec-injection advisory cluster (GHSA-gx55 / GHSA-wmgg /
// GHSA-v455 / GHSA-m63v / GHSA-qf5v). It walks every Fission CRD type via
// reflection, finds every tenant-reachable `*apiv1.PodSpec` and
// `*apiv1.Container` field (including slice element types), and asserts each
// is on the explicitly maintained `knownCovered` set. A new CRD addition that
// introduces a tenant-supplied PodSpec/Container surface without wiring it
// through ValidateForAdmission — or removes a surface without trimming
// knownCovered — fails this test.
//
// MessageQueueTrigger.Spec.PodSpec is included on knownCovered because it
// uses a controller-side allowlist (MergeAllowedPodSpecFields) plus its own
// admission validator (validateAllowedPodSpec in pkg/webhook/messagequeuetrigger.go).
// SecurityContext is not on that allowlist, so a tenant cannot inject
// capabilities through MQT — the qf5v fix doesn't need to touch that path.
func TestAllTenantContainerSurfacesAreValidated(t *testing.T) {
	knownCovered := map[string]string{
		"Function.Spec.PodSpec":              "ValidatePodSpecSafety in Function.validateForAdmission",
		"Environment.Spec.Runtime.PodSpec":   "ValidatePodSpecSafety in Environment.validateForAdmission",
		"Environment.Spec.Runtime.Container": "ValidateContainerSafety in Environment.validateForAdmission",
		"Environment.Spec.Builder.PodSpec":   "ValidatePodSpecSafety in Environment.validateForAdmission",
		"Environment.Spec.Builder.Container": "ValidateContainerSafety in Environment.validateForAdmission",
		"MessageQueueTrigger.Spec.PodSpec":   "MQT-specific allowlist (validateAllowedPodSpec); SecurityContext not allowlisted",
	}

	targets := map[reflect.Type]struct{}{
		reflect.TypeFor[apiv1.PodSpec]():   {},
		reflect.TypeFor[apiv1.Container](): {},
	}

	crdRoots := []any{
		Function{}, Environment{}, Package{}, HTTPTrigger{}, TimeTrigger{},
		KubernetesWatchTrigger{}, MessageQueueTrigger{}, CanaryConfig{},
	}

	found := map[string]struct{}{}
	for _, c := range crdRoots {
		rt := reflect.TypeOf(c)
		walkTenantPodSpecFields(rt.Name(), rt, targets, found, map[reflect.Type]bool{})
	}

	for path := range found {
		if _, ok := knownCovered[path]; !ok {
			t.Errorf("tenant-supplied PodSpec/Container field %q has no admission-time safety validator on record. "+
				"Wire it through ValidatePodSpecSafety / ValidateContainerSafety in the CRD's ValidateForAdmission (and sanitizeContainerSecurityContext at the merge site if applicable), then add the path to knownCovered in TestAllTenantContainerSurfacesAreValidated.",
				path)
		}
	}
	for path := range knownCovered {
		if _, ok := found[path]; !ok {
			t.Errorf("knownCovered references %q but reflection no longer finds it; the field was removed or renamed — trim knownCovered to match", path)
		}
	}
}

// walkTenantPodSpecFields recurses through a CRD type's exported struct fields
// (and slice/pointer element types) looking for fields whose underlying type
// matches one of `targets`. Each hit emits its dotted field path into `found`.
// Standard Kubernetes meta fields are skipped — they cannot carry tenant data.
func walkTenantPodSpecFields(prefix string, rt reflect.Type, targets map[reflect.Type]struct{}, found map[string]struct{}, seen map[reflect.Type]bool) {
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct || seen[rt] {
		return
	}
	seen[rt] = true
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Name == "TypeMeta" || f.Name == "ObjectMeta" || f.Name == "ListMeta" {
			continue
		}
		ft := f.Type
		// Unwrap pointer / slice / array to the underlying element type.
		for {
			switch ft.Kind() {
			case reflect.Pointer, reflect.Slice, reflect.Array:
				ft = ft.Elem()
				continue
			}
			break
		}
		path := prefix + "." + f.Name
		if _, hit := targets[ft]; hit {
			found[path] = struct{}{}
			continue // don't descend into PodSpec/Container — its internal fields are the upstream k8s API surface, not Fission-CRD-owned
		}
		if ft.Kind() == reflect.Struct {
			walkTenantPodSpecFields(path, ft, targets, found, seen)
		}
	}
}

// TestTenantContainerSurfaces_RejectSysAdmin pins, for every CRD surface on
// the knownCovered list, that ValidateForAdmission actually rejects a
// SYS_ADMIN injection at that exact field path. The reflection walk above
// catches *missing* wiring; this test catches wiring that exists but no longer
// rejects (e.g., a future refactor that forgets to call the safety validator).
func TestTenantContainerSurfaces_RejectSysAdmin(t *testing.T) {
	meta := metav1.ObjectMeta{Name: "tenant-attack", Namespace: "default"}
	sc := func() *apiv1.SecurityContext {
		return &apiv1.SecurityContext{
			Capabilities: &apiv1.Capabilities{Add: []apiv1.Capability{"SYS_ADMIN"}},
		}
	}
	psWithSysAdmin := func() *apiv1.PodSpec {
		return &apiv1.PodSpec{Containers: []apiv1.Container{{Name: "user", SecurityContext: sc()}}}
	}

	type admissionValidator interface {
		ValidateForAdmission() error
	}

	cases := []struct {
		path string
		mk   func() admissionValidator
	}{
		{
			path: "Function.Spec.PodSpec",
			mk: func() admissionValidator {
				f := &Function{ObjectMeta: meta}
				f.Spec.InvokeStrategy = InvokeStrategy{
					StrategyType:      StrategyTypeExecution,
					ExecutionStrategy: ExecutionStrategy{ExecutorType: ExecutorTypePoolmgr},
				}
				f.Spec.PodSpec = psWithSysAdmin()
				return f
			},
		},
		{
			path: "Environment.Spec.Runtime.Container",
			mk: func() admissionValidator {
				e := &Environment{ObjectMeta: meta}
				e.Spec.Version = 2
				e.Spec.Runtime.Container = &apiv1.Container{Name: "py", SecurityContext: sc()}
				return e
			},
		},
		{
			path: "Environment.Spec.Runtime.PodSpec",
			mk: func() admissionValidator {
				e := &Environment{ObjectMeta: meta}
				e.Spec.Version = 2
				e.Spec.Runtime.PodSpec = psWithSysAdmin()
				return e
			},
		},
		{
			path: "Environment.Spec.Builder.Container",
			mk: func() admissionValidator {
				e := &Environment{ObjectMeta: meta}
				e.Spec.Version = 2
				e.Spec.Builder.Container = &apiv1.Container{Name: "builder", SecurityContext: sc()}
				return e
			},
		},
		{
			path: "Environment.Spec.Builder.PodSpec",
			mk: func() admissionValidator {
				e := &Environment{ObjectMeta: meta}
				e.Spec.Version = 2
				e.Spec.Builder.PodSpec = psWithSysAdmin()
				return e
			},
		},
		// MessageQueueTrigger.Spec.PodSpec is intentionally NOT exercised here:
		// the MQT validator (pkg/webhook/messagequeuetrigger.go:validateAllowedPodSpec)
		// rejects on the disallowed-field surface (containers entirely),
		// not on capabilities. That coverage is pinned by the MQT webhook
		// tests (see pkg/webhook/messagequeuetrigger_test.go).
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			err := tc.mk().ValidateForAdmission()
			if err == nil {
				t.Fatalf("ValidateForAdmission must reject a SYS_ADMIN injection at %s, got nil", tc.path)
			}
			if !strings.Contains(err.Error(), "SYS_ADMIN") {
				t.Fatalf("error for %s must mention SYS_ADMIN, got: %v", tc.path, err)
			}
		})
	}
}
