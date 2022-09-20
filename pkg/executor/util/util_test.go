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
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func TestGetSpecFromConfigMap(t *testing.T) {

	kubeClient := fake.NewSimpleClientset()

	var permissionNum int64 = 10001
	var runAsNonRoot bool = true

	configMapData := make(map[string]string, 0)
	specPatch := `
securityContext:
  fsGroup: 10001
  runAsGroup: 10001
  runAsNonRoot: true
  runAsUser: 10001`

	configMapData["spec"] = specPatch

	testConfigMap := apiv1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config-map",
			Namespace: "fission",
		},
		Data: configMapData,
	}

	configmap, err := kubeClient.CoreV1().ConfigMaps("fission").Create(context.Background(), &testConfigMap, metav1.CreateOptions{})
	if err != nil {
		t.Errorf("Error creating configmap %v", err)
	}

	t.Logf("Configmap: %v", configmap.Data)

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
		cm      string
		cmns    string
		want    *apiv1.PodSpec
		wantErr bool
	}{
		{
			name:    "Configmap exists",
			cm:      "test-config-map",
			cmns:    "fission",
			want:    &testSpecPatch,
			wantErr: false,
		},
		{
			name:    "Configmap does not exists",
			cm:      "wrongname",
			cmns:    "fission",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "Wrong namespace",
			cm:      "test-config-map",
			cmns:    "fissio",
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetSpecFromConfigMap(context.Background(), kubeClient, tt.cm, tt.cmns)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetSpecFromConfigMap() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetSpecFromConfigMap() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetObjectReaperInterval(t *testing.T) {
	logger := loggerfactory.GetLogger()

	var want int

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
