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
	"log"
	"reflect"

	"github.com/hashicorp/go-multierror"
	"github.com/imdario/mergo"
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

	errs := &multierror.Error{}

	err := mergo.Merge(dst, src, mergo.WithAppendSlice, mergo.WithOverride)
	if err != nil {
		return nil, err
	}

	errs = multierror.Append(errs,
		checkSliceConflicts("Name", dst.Ports),
		checkSliceConflicts("Name", dst.Env),
		checkSliceConflicts("Name", dst.VolumeMounts),
		checkSliceConflicts("Name", dst.VolumeDevices))

	return dst, errs.ErrorOrNil()
}

func MergePodSpec(srcPodSpec *apiv1.PodSpec, targetPodSpec *apiv1.PodSpec) (*apiv1.PodSpec, error) {
	if targetPodSpec == nil {
		return srcPodSpec, nil
	}

	multierr := &multierror.Error{}

	// Get item from spec, if they exist in deployment - merge, else append
	// Same pattern for all lists (Mergo can not handle lists)
	// At some point this is better done with generics/reflection?
	err := mergeContainerLists(srcPodSpec, targetPodSpec)
	if err != nil {
		multierr = multierror.Append(multierr, err)
	}

	log.Printf("spec %v", srcPodSpec)

	err = mergeInitContainerList(srcPodSpec, targetPodSpec)
	if err != nil {
		multierr = multierror.Append(multierr, err)
	}

	// For volumes - if duplicate exist, throw error
	err = mergeVolumeLists(srcPodSpec, targetPodSpec)
	if err != nil {
		multierr = multierror.Append(multierr, err)
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

	//TODO - Security context should be merged instead of overriding.
	if targetPodSpec.SecurityContext != nil {
		srcPodSpec.SecurityContext = targetPodSpec.SecurityContext
	}

	//TODO - Affinity should be merged instead of overriding.
	if targetPodSpec.Affinity != nil {
		srcPodSpec.Affinity = targetPodSpec.Affinity
	}

	if targetPodSpec.Hostname != "" {
		srcPodSpec.Hostname = targetPodSpec.Hostname
	}

	for _, obj := range targetPodSpec.ImagePullSecrets {
		srcPodSpec.ImagePullSecrets = append(srcPodSpec.ImagePullSecrets, obj)
	}

	for _, obj := range targetPodSpec.Tolerations {
		srcPodSpec.Tolerations = append(srcPodSpec.Tolerations, obj)
	}

	for _, obj := range targetPodSpec.HostAliases {
		srcPodSpec.HostAliases = append(srcPodSpec.HostAliases, obj)
	}

	err = mergo.Merge(&srcPodSpec.NodeSelector, targetPodSpec.NodeSelector)
	if err != nil {
		multierr = multierror.Append(multierr, err)
	}

	return srcPodSpec, multierr.ErrorOrNil()
}

func mergeContainerLists(srcPodSpec *apiv1.PodSpec, targetPodSpec *apiv1.PodSpec) error {
	targetSpecContainers := targetPodSpec.Containers
	targetContainers := make(map[string]apiv1.Container)
	for _, c := range targetSpecContainers {
		targetContainers[c.Name] = c
	}

	multierr := &multierror.Error{}
	for i, c := range srcPodSpec.Containers {
		container, ok := targetContainers[c.Name]
		if ok {
			newC, err := MergeContainer(&c, &container)
			if err != nil {
				// record the error and continue
				multierr = multierror.Append(multierr, err)
			} else {
				srcPodSpec.Containers[i] = *newC
			}
			delete(targetContainers, c.Name)
		}
	}

	for _, container := range targetContainers {
		srcPodSpec.Containers = append(srcPodSpec.Containers, container)
	}

	log.Printf("loop merge: %v", srcPodSpec)

	return multierr.ErrorOrNil()
}

func mergeInitContainerList(srcPodSpec *apiv1.PodSpec, targetPodSpec *apiv1.PodSpec) error {
	targetSpecContainers := targetPodSpec.InitContainers
	targetContainers := make(map[string]apiv1.Container)
	for _, c := range targetSpecContainers {
		targetContainers[c.Name] = c
	}

	multierr := &multierror.Error{}
	for i, c := range srcPodSpec.InitContainers {
		container, ok := targetContainers[c.Name]
		if ok {
			newC, err := MergeContainer(&c, &container)
			if err != nil {
				// record the error and continue
				multierr = multierror.Append(multierr, err)
			} else {
				srcPodSpec.Containers[i] = *newC
			}
			delete(targetContainers, c.Name)
		}
	}

	for _, container := range targetContainers {
		srcPodSpec.InitContainers = append(srcPodSpec.InitContainers, container)
	}
	return multierr.ErrorOrNil()
}

func mergeVolumeLists(srcPodSpec *apiv1.PodSpec, targetPodSpec *apiv1.PodSpec) error {
	volumeList := targetPodSpec.Volumes
	specVolumes := make(map[string]apiv1.Volume)
	for _, vol := range volumeList {
		specVolumes[vol.Name] = vol
	}

	multierr := &multierror.Error{}
	for _, vol := range srcPodSpec.Volumes {
		_, ok := specVolumes[vol.Name]
		if ok {
			multierr = multierror.Append(multierr, errors.New("Duplicate volume name found in the spec"))
		} else {
			delete(specVolumes, vol.Name)
		}
	}

	for _, volume := range specVolumes {
		srcPodSpec.Volumes = append(srcPodSpec.Volumes, volume)
	}
	return multierr.ErrorOrNil()
}

func checkSliceConflicts(field string, objs interface{}) (err error) {
	defer func() {
		// just in case to recover from unknown error
		if e := recover(); e != nil {
			err = fmt.Errorf("error checking slice conflicts: %v", e)
		}
	}()

	if reflect.TypeOf(objs).Kind() != reflect.Slice {
		return fmt.Errorf("not a slice type: %v", reflect.TypeOf(objs))
	}

	errs := &multierror.Error{}
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
			errs = multierror.Append(errs, fmt.Errorf("duplicate name in %v: %v", objType, f.String()))
		} else {
			names[f.String()] = struct{}{}
		}
	}
	return errs.ErrorOrNil()
}
