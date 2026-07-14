// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
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
	var runAsNonRoot = true

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
	require.Equal(t, want, got, "Get default ObjectReaperInterval failed")
	want = 2
	os.Setenv("CONTAINER_OBJECT_REAPER_INTERVAL", fmt.Sprint(want))
	os.Unsetenv("OBJECT_REAPER_INTERVAL")
	got = GetObjectReaperInterval(logger, fv1.ExecutorTypeContainer, 5)
	require.Equal(t, want, got, "Get specific ObjectReaperInterval failed")

	// Test when only global reaper interval set
	want = 3
	os.Unsetenv("CONTAINER_OBJECT_REAPER_INTERVAL")
	os.Setenv("OBJECT_REAPER_INTERVAL", fmt.Sprint(want))
	got = GetObjectReaperInterval(logger, fv1.ExecutorTypeContainer, 5)
	require.Equal(t, want, got, "Get global ObjectReaperInterval failed")

	// Test when broken specific reaper interval set
	want = 4
	os.Setenv("CONTAINER_OBJECT_REAPER_INTERVAL", "just some string!")
	os.Unsetenv("OBJECT_REAPER_INTERVAL")
	got = GetObjectReaperInterval(logger, fv1.ExecutorTypeContainer, want)
	require.Equal(t, want, got)

	// Test when empty specific reaper interval set
	want = 5
	os.Setenv("CONTAINER_OBJECT_REAPER_INTERVAL", "")
	os.Unsetenv("OBJECT_REAPER_INTERVAL")
	got = GetObjectReaperInterval(logger, fv1.ExecutorTypeContainer, 5)
	require.Equal(t, want, got)
}

func TestAtoiOr(t *testing.T) {
	tests := []struct {
		name   string
		envVal string
		envSet bool
		def    int
		want   int
	}{
		{"unset returns default", "", false, 42, 42},
		{"empty string returns default", "", true, 42, 42},
		{"valid int returns parsed", "100", true, 42, 100},
		{"zero returns zero", "0", true, 42, 0},
		{"negative returns negative", "-5", true, 42, -5},
		{"garbage returns default", "abc", true, 42, 42},
		{"float string returns default", "3.14", true, 42, 42},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "TEST_ATOI_OR"
			if tt.envSet {
				t.Setenv(key, tt.envVal)
			} else {
				t.Setenv(key, "")
			}
			got := AtoiOr(key, tt.def)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDurOr(t *testing.T) {
	tests := []struct {
		name   string
		envVal string
		envSet bool
		def    time.Duration
		want   time.Duration
	}{
		{"unset returns default", "", false, 30 * time.Second, 30 * time.Second},
		{"empty string returns default", "", true, 30 * time.Second, 30 * time.Second},
		{"valid duration returns parsed", "1m", true, 30 * time.Second, time.Minute},
		{"seconds form", "45s", true, 30 * time.Second, 45 * time.Second},
		{"zero", "0", true, 30 * time.Second, 0},
		{"garbage returns default", "notaduration", true, 30 * time.Second, 30 * time.Second},
		{"complex form", "1h30m", true, 30 * time.Second, 90 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "TEST_DUR_OR"
			if tt.envSet {
				t.Setenv(key, tt.envVal)
			} else {
				t.Setenv(key, "")
			}
			got := DurOr(key, tt.def)
			require.Equal(t, tt.want, got)
		})
	}
}
