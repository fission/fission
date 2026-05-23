/*
Copyright 2016 The Fission Authors.

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
	"errors"
	"fmt"
	"reflect"

	"dario.cat/mergo"
	apiv1 "k8s.io/api/core/v1"
)

// TODO: replace functions here with native kubernetes strategic merge patch.
// https://kubernetes.io/docs/tasks/run-application/update-api-object-kubectl-patch/#use-a-strategic-merge-patch-to-update-a-deployment

// MergeContainer returns merged container specs.
// Slices are merged, and return an error if the elements in the slice have name conflicts.
// Maps are merged, the value of map of dst container are overridden if the key is the same.
// The rest of fields of dst container are overridden directly.
func MergeContainer(dst *apiv1.Container, src *apiv1.Container) (*apiv1.Container, error) {
	if src == nil {
		return dst, nil
	}

	// to prevent any modification to the original obj
	dstC := *dst

	var errs error
	err := mergo.Merge(&dstC, src, mergo.WithAppendSlice, mergo.WithOverride)
	if err != nil {
		return nil, err
	}
	errs = errors.Join(errs,
		checkSliceConflicts("Name", dstC.Ports),
		checkSliceConflicts("Name", dstC.Env),
		checkSliceConflicts("Name", dstC.VolumeMounts),
		checkSliceConflicts("Name", dstC.VolumeDevices))

	return &dstC, errs
}

// MergePodSpec updates srcPodSpec with targetPodSpec fields if not empty
func MergePodSpec(srcPodSpec *apiv1.PodSpec, targetPodSpec *apiv1.PodSpec) (*apiv1.PodSpec, error) {
	if targetPodSpec == nil {
		return srcPodSpec, nil
	}

	var multierr error

	// Get item from spec, if they exist in deployment - merge, else append
	// Same pattern for all lists (Mergo can not handle lists)
	// TODO: At some point this is better done with generics/reflection?
	cList, err := mergeContainerList(srcPodSpec.Containers, targetPodSpec.Containers)
	if err != nil {
		multierr = errors.Join(multierr, err)
	} else {
		srcPodSpec.Containers = cList
	}

	cList, err = mergeContainerList(srcPodSpec.InitContainers, targetPodSpec.InitContainers)
	if err != nil {
		multierr = errors.Join(multierr, err)
	} else {
		srcPodSpec.InitContainers = cList
	}

	// Sanitize per-container SecurityContext after the merge. The admission
	// webhook rejects privileged=true / allowPrivilegeEscalation=true and
	// dangerous capabilities (SYS_ADMIN, NET_ADMIN, etc.) at submit time,
	// but a webhook-bypass cluster (failurePolicy=Ignore or a stale object
	// from a pre-webhook upgrade window) could still reach this code path.
	// Strip the dangerous bits from the merged result so the resulting pod
	// cannot escape its container even if admission was bypassed. Closes
	// GHSA-gx55-f84r-v3r7 / GHSA-wmgg-3p4h-48x7 / GHSA-v455-mv2v-5g92.
	for i := range srcPodSpec.Containers {
		sanitizeContainerSecurityContext(&srcPodSpec.Containers[i])
	}
	for i := range srcPodSpec.InitContainers {
		sanitizeContainerSecurityContext(&srcPodSpec.InitContainers[i])
	}

	// For volumes - if duplicate exist, throw error. hostPath volumes are
	// stripped from the target before merge: a tenant-supplied hostPath
	// mount is a node-escape primitive (read /etc, the container runtime
	// socket, etc.). The admission webhook rejects them, and this layer
	// makes them unreachable even on webhook-bypass clusters. Closes
	// GHSA-gx55-f84r-v3r7 / GHSA-wmgg-3p4h-48x7 / GHSA-v455-mv2v-5g92.
	filteredTargetVols := stripHostPathVolumes(targetPodSpec.Volumes)
	vols, err := mergeVolumeLists(srcPodSpec.Volumes, filteredTargetVols)
	if err != nil {
		multierr = errors.Join(multierr, err)
	} else {
		srcPodSpec.Volumes = vols
	}

	if targetPodSpec.NodeName != "" {
		srcPodSpec.NodeName = targetPodSpec.NodeName
	}

	if targetPodSpec.Subdomain != "" {
		srcPodSpec.Subdomain = targetPodSpec.Subdomain
	}

	if targetPodSpec.SchedulerName != "" {
		srcPodSpec.SchedulerName = targetPodSpec.SchedulerName
	}

	if targetPodSpec.PriorityClassName != "" {
		srcPodSpec.PriorityClassName = targetPodSpec.PriorityClassName
	}

	if targetPodSpec.TerminationGracePeriodSeconds != nil {
		srcPodSpec.TerminationGracePeriodSeconds = targetPodSpec.TerminationGracePeriodSeconds
	}

	// Possibility to disable kubernetes environment variables for functions/environments (#1599)
	if targetPodSpec.EnableServiceLinks != nil {
		srcPodSpec.EnableServiceLinks = targetPodSpec.EnableServiceLinks
	}

	// TODO - Security context should be merged instead of overriding.
	// Pod-level SecurityContext IS propagated: the chart's
	// runtimePodSpec.podSpec.securityContext / builderPodSpec.podSpec.
	// securityContext are operator-supplied hardening (fsGroup,
	// runAsNonRoot=true, runAsUser=10001, runAsGroup=10001) that must
	// reach the pool / builder pods. The node-escape primitives flagged
	// by GHSA-gx55-f84r-v3r7 / GHSA-wmgg-3p4h-48x7 / GHSA-v455-mv2v-5g92
	// live at container-level (privileged, allowPrivilegeEscalation,
	// dangerous capabilities) and at pod level (hostNetwork, hostPID,
	// hostIPC, hostPath volumes, serviceAccountName override) — all of
	// which are denylisted in pkg/apis/core/v1/podspec_safety.go.
	if targetPodSpec.SecurityContext != nil {
		srcPodSpec.SecurityContext = targetPodSpec.SecurityContext
	}

	// TODO - Affinity should be merged instead of overriding.
	if targetPodSpec.Affinity != nil {
		srcPodSpec.Affinity = targetPodSpec.Affinity
	}

	if targetPodSpec.Hostname != "" {
		srcPodSpec.Hostname = targetPodSpec.Hostname
	}

	if targetPodSpec.RuntimeClassName != nil {
		srcPodSpec.RuntimeClassName = targetPodSpec.RuntimeClassName
	}

	if targetPodSpec.RestartPolicy != "" {
		srcPodSpec.RestartPolicy = targetPodSpec.RestartPolicy
	}

	if targetPodSpec.ActiveDeadlineSeconds != nil {
		srcPodSpec.ActiveDeadlineSeconds = targetPodSpec.ActiveDeadlineSeconds
	}

	if targetPodSpec.DNSPolicy != "" {
		srcPodSpec.DNSPolicy = targetPodSpec.DNSPolicy
	}

	// ServiceAccountName / DeprecatedServiceAccount intentionally not
	// propagated: the controller chooses the SA for the pod
	// (fission-fetcher for runtime pods, fission-builder for build pods).
	// Letting a user-supplied podspec override it would defeat the SA-token
	// scoping introduced by GHSA-85g2-pmrx-r49q and GHSA-8wcj-mfrc-jx5q.
	// Closes GHSA-gx55-f84r-v3r7 / GHSA-wmgg-3p4h-48x7 / GHSA-v455-mv2v-5g92.

	if targetPodSpec.AutomountServiceAccountToken != nil {
		srcPodSpec.AutomountServiceAccountToken = targetPodSpec.AutomountServiceAccountToken
	}

	// HostNetwork / HostPID / HostIPC intentionally not propagated.
	// A pod sharing host namespaces is a node-escape primitive — the
	// admission webhook rejects these fields, and this layer makes them
	// unreachable even on webhook-bypass clusters.
	// Closes GHSA-gx55-f84r-v3r7 / GHSA-wmgg-3p4h-48x7 / GHSA-v455-mv2v-5g92.

	if targetPodSpec.ShareProcessNamespace != nil {
		srcPodSpec.ShareProcessNamespace = targetPodSpec.ShareProcessNamespace
	}

	if targetPodSpec.PriorityClassName != "" {
		srcPodSpec.PriorityClassName = targetPodSpec.PriorityClassName
	}

	if targetPodSpec.Priority != nil {
		srcPodSpec.Priority = targetPodSpec.Priority
	}

	if targetPodSpec.PreemptionPolicy != nil {
		srcPodSpec.PreemptionPolicy = targetPodSpec.PreemptionPolicy
	}

	if targetPodSpec.EnableServiceLinks != nil {
		srcPodSpec.EnableServiceLinks = targetPodSpec.EnableServiceLinks
	}

	srcPodSpec.ImagePullSecrets = append(srcPodSpec.ImagePullSecrets, targetPodSpec.ImagePullSecrets...)
	srcPodSpec.Tolerations = append(srcPodSpec.Tolerations, targetPodSpec.Tolerations...)
	srcPodSpec.HostAliases = append(srcPodSpec.HostAliases, targetPodSpec.HostAliases...)

	err = mergo.Merge(&srcPodSpec.NodeSelector, targetPodSpec.NodeSelector)
	if err != nil {
		multierr = errors.Join(multierr, err)
	}

	return srcPodSpec, multierr
}

func mergeContainerList(dst []apiv1.Container, src []apiv1.Container) ([]apiv1.Container, error) {
	var errs error

	list := append(dst, src...)
	containers := make(map[string]*apiv1.Container, len(list))

	for i, c := range list {
		container, ok := containers[c.Name]
		if ok {
			newC, err := MergeContainer(container, &c)
			if err != nil {
				// record the error and continue
				errs = errors.Join(errs, err)
			} else {
				containers[c.Name] = newC
			}
		} else {
			containers[c.Name] = &list[i]
		}
	}

	var containerList []apiv1.Container
	for _, c := range containers {
		containerList = append(containerList, *c)
	}

	if errs != nil {
		return nil, errs
	}

	return containerList, nil
}

func mergeVolumeLists(dst []apiv1.Volume, src []apiv1.Volume) ([]apiv1.Volume, error) {
	dst = append(dst, src...)
	err := checkSliceConflicts("Name", dst)
	if err != nil {
		return nil, err
	}
	return dst, err
}

func checkSliceConflicts(field string, objs any) (err error) {
	defer func() {
		// just in case to recover from unknown error
		if e := recover(); e != nil {
			err = fmt.Errorf("error checking slice conflicts: %v", e)
		}
	}()

	if reflect.TypeOf(objs).Kind() != reflect.Slice {
		return fmt.Errorf("not a slice type: %v", reflect.TypeOf(objs))
	}

	var errs error
	names := make(map[string]struct{})

	s := reflect.ValueOf(objs)
	var elemType reflect.Type

	for i := 0; i < s.Len(); i++ {
		r := s.Index(i)

		// if objs pass in is a slice of interface{} ([]interface{}), then
		// use Elem() to get element value.
		if r.Kind() == reflect.Interface {
			r = r.Elem()
		}
		objType := reflect.Indirect(r).Type()

		if elemType == nil {
			elemType = objType
		} else if objType != elemType {
			return fmt.Errorf("unable to check conflict between different types: %v, %v", elemType, objType)
		}

		f := reflect.Indirect(r).FieldByName(field)
		if !f.IsValid() {
			return fmt.Errorf("cannot compare type without target field: %v %v", objType, field)
		}

		_, ok := names[f.String()]
		if ok {
			errs = errors.Join(errs, fmt.Errorf("duplicate name in %v: %v", objType, f.String()))
		} else {
			names[f.String()] = struct{}{}
		}
	}
	return errs
}

// stripHostPathVolumes returns a copy of vols with any volume whose source
// is a hostPath removed. Defense in depth — the admission webhook already
// rejects hostPath in tenant-supplied podspecs (see
// pkg/apis/core/v1/podspec_safety.go), but on webhook-bypass clusters
// (failurePolicy=Ignore, or stale objects from a pre-webhook upgrade
// window) this layer makes the dangerous primitive unreachable.
// Closes GHSA-gx55-f84r-v3r7 / GHSA-wmgg-3p4h-48x7 / GHSA-v455-mv2v-5g92.
func stripHostPathVolumes(vols []apiv1.Volume) []apiv1.Volume {
	if len(vols) == 0 {
		return vols
	}
	out := make([]apiv1.Volume, 0, len(vols))
	for _, v := range vols {
		if v.HostPath != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}

// dangerousMergeContainerCapabilities lists the Linux capabilities that
// effectively bypass the container sandbox. Kept in sync with the
// authoritative denylist in pkg/apis/core/v1/podspec_safety.go — the
// admission webhook is the primary defence; this is the merge-layer
// belt-and-braces for webhook-bypass clusters.
var dangerousMergeContainerCapabilities = map[apiv1.Capability]struct{}{
	"SYS_ADMIN":       {},
	"NET_ADMIN":       {},
	"SYS_PTRACE":      {},
	"SYS_MODULE":      {},
	"DAC_READ_SEARCH": {},
	"DAC_OVERRIDE":    {},
}

// sanitizeContainerSecurityContext zeroes out the privilege-escalation bits
// of a container's SecurityContext after a MergePodSpec call. The merge
// path uses mergo.WithOverride and unconditionally copies SecurityContext
// fields from the target container, so a tenant-supplied podspec with
// privileged=true / allowPrivilegeEscalation=true / Capabilities.Add =
// [SYS_ADMIN, ...] would otherwise reach the running pod on
// webhook-bypass clusters (failurePolicy=Ignore or stale objects from a
// pre-webhook upgrade). The webhook is the primary defence; this is
// defence in depth. Closes GHSA-gx55-f84r-v3r7 / GHSA-wmgg-3p4h-48x7 /
// GHSA-v455-mv2v-5g92.
func sanitizeContainerSecurityContext(c *apiv1.Container) {
	if c.SecurityContext == nil {
		return
	}
	// Deep-copy before mutating. MergeContainer does a shallow struct copy
	// (`dstC := *dst`) and mergo.WithOverride aliases src.SecurityContext
	// onto dstC.SecurityContext, so mutating in place would leak into the
	// caller's targetPodSpec — which is typically env.Spec.Runtime.PodSpec
	// from an informer cache. Allocating a fresh SecurityContext (and a
	// fresh Capabilities.Add slice via a new backing array) keeps the
	// sanitization local to the merged result.
	c.SecurityContext = c.SecurityContext.DeepCopy()
	sc := c.SecurityContext
	if sc.Privileged != nil && *sc.Privileged {
		sc.Privileged = new(false)
	}
	if sc.AllowPrivilegeEscalation != nil && *sc.AllowPrivilegeEscalation {
		sc.AllowPrivilegeEscalation = new(false)
	}
	if sc.Capabilities != nil && len(sc.Capabilities.Add) > 0 {
		filtered := make([]apiv1.Capability, 0, len(sc.Capabilities.Add))
		for _, cap := range sc.Capabilities.Add {
			if _, bad := dangerousMergeContainerCapabilities[cap]; bad {
				continue
			}
			filtered = append(filtered, cap)
		}
		sc.Capabilities.Add = filtered
	}
}
