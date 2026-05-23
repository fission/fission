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

package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestMergeAllowedPodSpecFieldsDropsImageOverride(t *testing.T) {
	src := &apiv1.PodSpec{
		Containers: []apiv1.Container{{Name: "connector", Image: "fission/keda-kafka:1.0"}},
	}
	user := &apiv1.PodSpec{
		Containers:   []apiv1.Container{{Name: "connector", Image: "evil/registry:latest"}},
		NodeSelector: map[string]string{"role": "mqt"},
	}

	out, dropped := MergeAllowedPodSpecFields(src, user)

	assert.Equal(t, "fission/keda-kafka:1.0", out.Containers[0].Image,
		"user-supplied container image must NOT override controller-set image")
	assert.Equal(t, map[string]string{"role": "mqt"}, out.NodeSelector,
		"node selector must flow through")
	assert.Contains(t, dropped, "containers[].image", "image override must be reported as dropped")
}

func TestMergeAllowedPodSpecFieldsAcceptsTolerations(t *testing.T) {
	src := &apiv1.PodSpec{
		Containers: []apiv1.Container{{Name: "connector"}},
	}
	user := &apiv1.PodSpec{
		Tolerations: []apiv1.Toleration{{Key: "dedicated", Operator: apiv1.TolerationOpEqual, Value: "mqt"}},
	}
	out, dropped := MergeAllowedPodSpecFields(src, user)
	require.Len(t, out.Tolerations, 1)
	assert.Empty(t, dropped, "tolerations are allowlisted and must not be reported as dropped")
}

func TestMergeAllowedPodSpecFieldsAcceptsAffinityAndRuntimeClass(t *testing.T) {
	src := &apiv1.PodSpec{Containers: []apiv1.Container{{Name: "connector"}}}
	rc := "kata"
	aff := &apiv1.Affinity{
		NodeAffinity: &apiv1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &apiv1.NodeSelector{
				NodeSelectorTerms: []apiv1.NodeSelectorTerm{{
					MatchExpressions: []apiv1.NodeSelectorRequirement{{
						Key:      "topology.kubernetes.io/zone",
						Operator: apiv1.NodeSelectorOpIn,
						Values:   []string{"us-east-1a"},
					}},
				}},
			},
		},
	}
	user := &apiv1.PodSpec{
		Affinity:         aff,
		RuntimeClassName: &rc,
	}
	out, _ := MergeAllowedPodSpecFields(src, user)
	require.NotNil(t, out.Affinity)
	require.NotNil(t, out.RuntimeClassName)
	assert.Equal(t, "kata", *out.RuntimeClassName)
}

func TestMergeAllowedPodSpecFieldsAppliesContainerResources(t *testing.T) {
	src := &apiv1.PodSpec{
		Containers: []apiv1.Container{{Name: "connector", Image: "fission/keda-kafka:1.0"}},
	}
	user := &apiv1.PodSpec{
		Containers: []apiv1.Container{{
			Name: "connector",
			Resources: apiv1.ResourceRequirements{
				Requests: apiv1.ResourceList{apiv1.ResourceCPU: resource.MustParse("100m")},
				Limits:   apiv1.ResourceList{apiv1.ResourceMemory: resource.MustParse("128Mi")},
			},
		}},
	}
	out, dropped := MergeAllowedPodSpecFields(src, user)
	assert.Equal(t, "fission/keda-kafka:1.0", out.Containers[0].Image,
		"image must remain controller-set after Resources merge")
	assert.Equal(t, resource.MustParse("100m"), out.Containers[0].Resources.Requests[apiv1.ResourceCPU])
	assert.Equal(t, resource.MustParse("128Mi"), out.Containers[0].Resources.Limits[apiv1.ResourceMemory])
	assert.Empty(t, dropped, "name-matched Resources must not be reported as dropped")
}

func TestMergeAllowedPodSpecFieldsDropsDangerousFields(t *testing.T) {
	src := &apiv1.PodSpec{
		Containers: []apiv1.Container{{Name: "connector", Image: "fission/keda-kafka:1.0"}},
	}
	runAsUser := int64(0)
	automount := true
	share := true
	gracePeriod := int64(60)
	deadline := int64(120)
	priority := int32(1000)
	enableLinks := true
	preempt := apiv1.PreemptNever
	fqdn := true
	hostUsers := false
	user := &apiv1.PodSpec{
		Containers: []apiv1.Container{{
			Name:            "connector",
			Image:           "evil:latest",
			Command:         []string{"/bin/sh"},
			Args:            []string{"-c", "curl evil"},
			Env:             []apiv1.EnvVar{{Name: "INJECTED", Value: "yes"}},
			EnvFrom:         []apiv1.EnvFromSource{{SecretRef: &apiv1.SecretEnvSource{LocalObjectReference: apiv1.LocalObjectReference{Name: "any-secret"}}}},
			VolumeMounts:    []apiv1.VolumeMount{{Name: "host", MountPath: "/host"}},
			Ports:           []apiv1.ContainerPort{{ContainerPort: 1234}},
			WorkingDir:      "/evil",
			Lifecycle:       &apiv1.Lifecycle{},
			LivenessProbe:   &apiv1.Probe{},
			ReadinessProbe:  &apiv1.Probe{},
			StartupProbe:    &apiv1.Probe{},
			SecurityContext: &apiv1.SecurityContext{RunAsUser: &runAsUser},
		}},
		InitContainers:                []apiv1.Container{{Name: "init", Image: "evil:latest"}},
		Volumes:                       []apiv1.Volume{{Name: "host", VolumeSource: apiv1.VolumeSource{HostPath: &apiv1.HostPathVolumeSource{Path: "/"}}}},
		ImagePullSecrets:              []apiv1.LocalObjectReference{{Name: "creds"}},
		ServiceAccountName:            "cluster-admin",
		AutomountServiceAccountToken:  &automount,
		NodeName:                      "evil-node",
		HostNetwork:                   true,
		HostPID:                       true,
		HostIPC:                       true,
		ShareProcessNamespace:         &share,
		SecurityContext:               &apiv1.PodSecurityContext{RunAsUser: &runAsUser},
		SchedulerName:                 "evil-scheduler",
		HostAliases:                   []apiv1.HostAlias{{IP: "1.2.3.4", Hostnames: []string{"evil"}}},
		PriorityClassName:             "high",
		DNSConfig:                     &apiv1.PodDNSConfig{},
		TopologySpreadConstraints:     []apiv1.TopologySpreadConstraint{{}},
		RestartPolicy:                 apiv1.RestartPolicyNever,
		TerminationGracePeriodSeconds: &gracePeriod,
		ActiveDeadlineSeconds:         &deadline,
		DNSPolicy:                     apiv1.DNSClusterFirst,
		Hostname:                      "evil-host",
		Subdomain:                     "evil",
		Priority:                      &priority,
		ReadinessGates:                []apiv1.PodReadinessGate{{ConditionType: "evil"}},
		EnableServiceLinks:            &enableLinks,
		PreemptionPolicy:              &preempt,
		Overhead:                      apiv1.ResourceList{apiv1.ResourceCPU: resource.MustParse("10m")},
		SetHostnameAsFQDN:             &fqdn,
		OS:                            &apiv1.PodOS{Name: apiv1.Linux},
		HostUsers:                     &hostUsers,
		SchedulingGates:               []apiv1.PodSchedulingGate{{Name: "evil"}},
		ResourceClaims:                []apiv1.PodResourceClaim{{Name: "evil"}},
	}
	out, dropped := MergeAllowedPodSpecFields(src, user)

	// Output must look identical to src (modulo nil vs empty).
	assert.Equal(t, "fission/keda-kafka:1.0", out.Containers[0].Image)
	assert.Empty(t, out.Containers[0].Command)
	assert.Empty(t, out.Containers[0].Args)
	assert.Empty(t, out.Containers[0].Env)
	assert.Empty(t, out.Containers[0].EnvFrom)
	assert.Empty(t, out.Containers[0].VolumeMounts)
	assert.Empty(t, out.Containers[0].Ports)
	assert.Empty(t, out.Containers[0].WorkingDir)
	assert.Nil(t, out.Containers[0].Lifecycle)
	assert.Nil(t, out.Containers[0].LivenessProbe)
	assert.Nil(t, out.Containers[0].ReadinessProbe)
	assert.Nil(t, out.Containers[0].StartupProbe)
	assert.Nil(t, out.Containers[0].SecurityContext)
	assert.Empty(t, out.InitContainers)
	assert.Empty(t, out.Volumes)
	assert.Empty(t, out.ImagePullSecrets)
	assert.Empty(t, out.ServiceAccountName)
	assert.Nil(t, out.AutomountServiceAccountToken)
	assert.Empty(t, out.NodeName)
	assert.False(t, out.HostNetwork)
	assert.False(t, out.HostPID)
	assert.False(t, out.HostIPC)
	assert.Nil(t, out.ShareProcessNamespace)
	assert.Nil(t, out.SecurityContext)
	assert.Empty(t, out.SchedulerName)
	assert.Empty(t, out.HostAliases)
	assert.Empty(t, out.PriorityClassName)
	assert.Nil(t, out.DNSConfig)
	assert.Empty(t, out.TopologySpreadConstraints)
	assert.Empty(t, out.RestartPolicy)
	assert.Nil(t, out.TerminationGracePeriodSeconds)
	assert.Nil(t, out.ActiveDeadlineSeconds)
	assert.Empty(t, out.DNSPolicy)
	assert.Empty(t, out.Hostname)
	assert.Empty(t, out.Subdomain)
	assert.Nil(t, out.Priority)
	assert.Empty(t, out.ReadinessGates)
	assert.Nil(t, out.EnableServiceLinks)
	assert.Nil(t, out.PreemptionPolicy)
	assert.Empty(t, out.Overhead)
	assert.Nil(t, out.SetHostnameAsFQDN)
	assert.Nil(t, out.OS)
	assert.Nil(t, out.HostUsers)
	assert.Empty(t, out.SchedulingGates)
	assert.Empty(t, out.ResourceClaims)

	for _, want := range []string{
		"containers[].image",
		"containers[].command",
		"containers[].args",
		"containers[].env",
		"containers[].envFrom",
		"containers[].volumeMounts",
		"containers[].ports",
		"containers[].workingDir",
		"containers[].lifecycle",
		"containers[].livenessProbe",
		"containers[].readinessProbe",
		"containers[].startupProbe",
		"containers[].securityContext",
		"initContainers",
		"volumes",
		"imagePullSecrets",
		"serviceAccountName",
		"automountServiceAccountToken",
		"nodeName",
		"hostNetwork",
		"hostPID",
		"hostIPC",
		"shareProcessNamespace",
		"securityContext",
		"schedulerName",
		"hostAliases",
		"priorityClassName",
		"dnsConfig",
		"topologySpreadConstraints",
		"restartPolicy",
		"terminationGracePeriodSeconds",
		"activeDeadlineSeconds",
		"dnsPolicy",
		"hostname",
		"subdomain",
		"priority",
		"readinessGates",
		"enableServiceLinks",
		"preemptionPolicy",
		"overhead",
		"setHostnameAsFQDN",
		"os",
		"hostUsers",
		"schedulingGates",
		"resourceClaims",
	} {
		assert.Contains(t, dropped, want, "expected %q to be reported as dropped", want)
	}
}

func TestMergeAllowedPodSpecFieldsNilUser(t *testing.T) {
	src := &apiv1.PodSpec{
		Containers: []apiv1.Container{{Name: "connector", Image: "fission/keda-kafka:1.0"}},
	}
	out, dropped := MergeAllowedPodSpecFields(src, nil)
	assert.Empty(t, dropped)
	assert.Equal(t, "fission/keda-kafka:1.0", out.Containers[0].Image)
}

func TestMergeAllowedPodSpecFieldsDoesNotMutateSrc(t *testing.T) {
	src := &apiv1.PodSpec{
		Containers:   []apiv1.Container{{Name: "connector", Image: "fission/keda-kafka:1.0"}},
		NodeSelector: map[string]string{"existing": "label"},
	}
	user := &apiv1.PodSpec{
		NodeSelector: map[string]string{"new": "label"},
	}
	_, _ = MergeAllowedPodSpecFields(src, user)
	// src must remain unchanged so the caller can re-use it across reconciles.
	assert.Equal(t, map[string]string{"existing": "label"}, src.NodeSelector)
}

func TestDisallowedPodSpecFieldsAllPresent(t *testing.T) {
	runAsUser := int64(0)
	automount := true
	share := true
	gracePeriod := int64(60)
	deadline := int64(120)
	priority := int32(1000)
	enableLinks := true
	preempt := apiv1.PreemptNever
	fqdn := true
	hostUsers := false
	ps := &apiv1.PodSpec{
		Containers: []apiv1.Container{{
			Name:            "connector",
			Image:           "evil:latest",
			Command:         []string{"/bin/sh"},
			Args:            []string{"-c", "curl evil"},
			Env:             []apiv1.EnvVar{{Name: "INJECTED", Value: "yes"}},
			EnvFrom:         []apiv1.EnvFromSource{{SecretRef: &apiv1.SecretEnvSource{LocalObjectReference: apiv1.LocalObjectReference{Name: "any-secret"}}}},
			VolumeMounts:    []apiv1.VolumeMount{{Name: "host", MountPath: "/host"}},
			Ports:           []apiv1.ContainerPort{{ContainerPort: 1234}},
			WorkingDir:      "/evil",
			Lifecycle:       &apiv1.Lifecycle{},
			LivenessProbe:   &apiv1.Probe{},
			ReadinessProbe:  &apiv1.Probe{},
			StartupProbe:    &apiv1.Probe{},
			SecurityContext: &apiv1.SecurityContext{RunAsUser: &runAsUser},
		}},
		InitContainers:                []apiv1.Container{{Name: "init", Image: "evil:latest"}},
		Volumes:                       []apiv1.Volume{{Name: "host", VolumeSource: apiv1.VolumeSource{HostPath: &apiv1.HostPathVolumeSource{Path: "/"}}}},
		ImagePullSecrets:              []apiv1.LocalObjectReference{{Name: "creds"}},
		ServiceAccountName:            "cluster-admin",
		AutomountServiceAccountToken:  &automount,
		NodeName:                      "evil-node",
		HostNetwork:                   true,
		HostPID:                       true,
		HostIPC:                       true,
		ShareProcessNamespace:         &share,
		SecurityContext:               &apiv1.PodSecurityContext{RunAsUser: &runAsUser},
		SchedulerName:                 "evil-scheduler",
		HostAliases:                   []apiv1.HostAlias{{IP: "1.2.3.4", Hostnames: []string{"evil"}}},
		PriorityClassName:             "high",
		DNSConfig:                     &apiv1.PodDNSConfig{},
		TopologySpreadConstraints:     []apiv1.TopologySpreadConstraint{{}},
		RestartPolicy:                 apiv1.RestartPolicyNever,
		TerminationGracePeriodSeconds: &gracePeriod,
		ActiveDeadlineSeconds:         &deadline,
		DNSPolicy:                     apiv1.DNSClusterFirst,
		Hostname:                      "evil-host",
		Subdomain:                     "evil",
		Priority:                      &priority,
		ReadinessGates:                []apiv1.PodReadinessGate{{ConditionType: "evil"}},
		EnableServiceLinks:            &enableLinks,
		PreemptionPolicy:              &preempt,
		Overhead:                      apiv1.ResourceList{apiv1.ResourceCPU: resource.MustParse("10m")},
		SetHostnameAsFQDN:             &fqdn,
		OS:                            &apiv1.PodOS{Name: apiv1.Linux},
		HostUsers:                     &hostUsers,
		SchedulingGates:               []apiv1.PodSchedulingGate{{Name: "evil"}},
		ResourceClaims:                []apiv1.PodResourceClaim{{Name: "evil"}},
	}

	bad := DisallowedPodSpecFields(ps)

	for _, want := range []string{
		"containers[].image",
		"containers[].command",
		"containers[].args",
		"containers[].env",
		"containers[].envFrom",
		"containers[].volumeMounts",
		"containers[].ports",
		"containers[].workingDir",
		"containers[].lifecycle",
		"containers[].livenessProbe",
		"containers[].readinessProbe",
		"containers[].startupProbe",
		"containers[].securityContext",
		"initContainers",
		"volumes",
		"imagePullSecrets",
		"serviceAccountName",
		"automountServiceAccountToken",
		"nodeName",
		"hostNetwork",
		"hostPID",
		"hostIPC",
		"shareProcessNamespace",
		"securityContext",
		"schedulerName",
		"hostAliases",
		"priorityClassName",
		"dnsConfig",
		"topologySpreadConstraints",
		"restartPolicy",
		"terminationGracePeriodSeconds",
		"activeDeadlineSeconds",
		"dnsPolicy",
		"hostname",
		"subdomain",
		"priority",
		"readinessGates",
		"enableServiceLinks",
		"preemptionPolicy",
		"overhead",
		"setHostnameAsFQDN",
		"os",
		"hostUsers",
		"schedulingGates",
		"resourceClaims",
	} {
		assert.Contains(t, bad, want, "expected %q to be reported as disallowed", want)
	}
}

func TestDisallowedPodSpecFieldsOnlyAllowed(t *testing.T) {
	rc := "kata"
	ps := &apiv1.PodSpec{
		Containers:       []apiv1.Container{{Name: "connector"}},
		NodeSelector:     map[string]string{"role": "mqt"},
		Tolerations:      []apiv1.Toleration{{Key: "dedicated"}},
		Affinity:         &apiv1.Affinity{},
		RuntimeClassName: &rc,
	}
	assert.Empty(t, DisallowedPodSpecFields(ps),
		"PodSpec containing only allowlisted fields must report no disallowed entries")
}

func TestDisallowedPodSpecFieldsDedupAcrossContainers(t *testing.T) {
	// Two containers each set Image — the helper must report
	// "containers[].image" once, not twice.
	ps := &apiv1.PodSpec{
		Containers: []apiv1.Container{
			{Name: "c1", Image: "a:1", Command: []string{"sh"}},
			{Name: "c2", Image: "b:2", Command: []string{"bash"}},
		},
	}

	bad := DisallowedPodSpecFields(ps)

	// Each disallowed field name must appear exactly once.
	counts := map[string]int{}
	for _, name := range bad {
		counts[name]++
	}
	assert.Equal(t, 1, counts["containers[].image"],
		"image violation must be deduped across containers, got %v", bad)
	assert.Equal(t, 1, counts["containers[].command"],
		"command violation must be deduped across containers, got %v", bad)
}

func TestDisallowedPodSpecFieldsNil(t *testing.T) {
	assert.Nil(t, DisallowedPodSpecFields(nil))
}
