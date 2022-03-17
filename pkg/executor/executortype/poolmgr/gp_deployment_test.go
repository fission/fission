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

package poolmgr

import (
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestGetPoolName(t *testing.T) {
	tests := []struct {
		name string
		env  *fv1.Environment
		want string
	}{
		{
			"Under character limit",
			&fv1.Environment{
				TypeMeta: metav1.TypeMeta{
					Kind:       fv1.CRD_NAME_ENVIRONMENT,
					APIVersion: fv1.CRD_VERSION,
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:            "test",
					Namespace:       "testns",
					ResourceVersion: "2517",
				},
			},
			"poolmgr-test-testns-2517",
		},
		{
			"Over character limit",
			&fv1.Environment{
				TypeMeta: metav1.TypeMeta{
					Kind:       fv1.CRD_NAME_ENVIRONMENT,
					APIVersion: fv1.CRD_VERSION,
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:            "justtryingtoincreasethenumberofcharactersinthisstring",
					Namespace:       "checkingifthegetpoolfunctionworkswithcharactersmorethan18",
					ResourceVersion: "2518",
				},
			},
			"poolmgr-justtryingtoincrea-checkingifthegetpo-2518",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getPoolName(tt.env); got != tt.want {
				t.Errorf("getPoolName() = %s, want = %s len(getPoolName()) = %x len(want) = %x", got, tt.want, len(got), len(tt.want))
			} else {
				fmt.Printf("getPoolName() = %s,length of string = %x", got, len(got))
			}
		})
	}
}
