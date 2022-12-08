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
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	apiv1 "k8s.io/api/core/v1"
)

func TestGetSpecFromConfigMap(t *testing.T) {
	runtimePodSpecPath := "runtime-podspec-patch.yaml"
	tempDir := t.TempDir()
	specPatch := `
securityContext:
  fsGroup: 10001
  runAsGroup: 10001
  runAsNonRoot: true
  runAsUser: 10001`
	err := os.WriteFile(tempDir+"/"+runtimePodSpecPath, []byte(specPatch), 0644)
	if err != nil {
		t.Errorf("Error writing to file %s", err)
	}
	specPatch2 := `
securityContext2:
securityContext:
  fsGroup: "invalida_input"
  runAsGroup: 10001
  runAsNonRoot: true
  runAsUser: 10001`
	err = os.WriteFile(tempDir+"/"+runtimePodSpecPath+"2", []byte(specPatch2), 0644)
	if err != nil {
		t.Errorf("Error writing to file %s", err)
	}

	var permissionNum int64 = 10001
	var runAsNonRoot bool = true

	testSpecPatch := apiv1.PodSpec{
		SecurityContext: &apiv1.PodSecurityContext{
			FSGroup:      &permissionNum,
			RunAsGroup:   &permissionNum,
			RunAsNonRoot: &runAsNonRoot,
			RunAsUser:    &permissionNum,
		},
	}
	tests := []struct {
		name    string
		path    string
		want    *apiv1.PodSpec
		wantErr bool
	}{
		{
			name:    "File exists with valid data",
			path:    tempDir + "/" + runtimePodSpecPath,
			want:    &testSpecPatch,
			wantErr: false,
		},
		{
			name:    "File with invalid data",
			path:    tempDir + "/" + runtimePodSpecPath + "2",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "File does not exist",
			path:    tempDir + "/" + "notexist",
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetSpecFromConfigMap(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetSpecFromConfigMap() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetSpecFromConfigMap() diff = %s", cmp.Diff(tt.want, got))
			}
		})
	}
}

func TestGetObjectReaperInterval(t *testing.T) {
	logger := loggerfactory.GetLogger()

	var want uint

	// Test default reaper interval
	want = 1
	got := GetObjectReaperInterval(logger, fv1.ExecutorTypeContainer, want)
	if want != got {
		t.Fatalf(`Get default ObjectReaperInterval failed. Want %d, Got %d`, want, got)
	}

	// Test when only specific reaper interval set
	want = 2
	os.Setenv("CONTAINER_OBJECT_REAPER_INTERVAL", fmt.Sprint(want))
	os.Unsetenv("OBJECT_REAPER_INTERVAL")
	got = GetObjectReaperInterval(logger, fv1.ExecutorTypeContainer, 5)
	if want != got {
		t.Fatalf(`%d %d`, want, got)
	}

	// Test when only global reaper interval set
	want = 3
	os.Unsetenv("CONTAINER_OBJECT_REAPER_INTERVAL")
	os.Setenv("OBJECT_REAPER_INTERVAL", fmt.Sprint(want))
	got = GetObjectReaperInterval(logger, fv1.ExecutorTypeContainer, 5)
	if want != got {
		t.Fatalf(`%d %d`, want, got)
	}

	// Test when broken specific reaper interval set
	want = 4
	os.Setenv("CONTAINER_OBJECT_REAPER_INTERVAL", "just some string!")
	os.Unsetenv("OBJECT_REAPER_INTERVAL")
	got = GetObjectReaperInterval(logger, fv1.ExecutorTypeContainer, want)
	if want != got {
		t.Fatalf(`%d %d`, want, got)
	}

	// Test when empty specific reaper interval set
	want = 5
	os.Setenv("CONTAINER_OBJECT_REAPER_INTERVAL", "")
	os.Unsetenv("OBJECT_REAPER_INTERVAL")
	got = GetObjectReaperInterval(logger, fv1.ExecutorTypeContainer, 5)
	if want != got {
		t.Fatalf(`%d %d`, want, got)
	}
}
