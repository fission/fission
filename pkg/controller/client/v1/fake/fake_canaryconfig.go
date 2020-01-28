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

package fake

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	v1 "github.com/fission/fission/pkg/controller/client/v1"
)

type (
	FakeCanaryConfig struct{}
)

func newCanaryConfigClient(c *v1.V1) v1.CanaryConfigInterface {
	return &FakeCanaryConfig{}
}

func (c *FakeCanaryConfig) Create(canaryConf *fv1.CanaryConfig) (*metav1.ObjectMeta, error) {
	return nil, nil
}

func (c *FakeCanaryConfig) Get(m *metav1.ObjectMeta) (*fv1.CanaryConfig, error) {
	return nil, nil
}

func (c *FakeCanaryConfig) Update(canaryConf *fv1.CanaryConfig) (*metav1.ObjectMeta, error) {
	return nil, nil
}

func (c *FakeCanaryConfig) Delete(m *metav1.ObjectMeta) error {
	return nil
}

func (c *FakeCanaryConfig) List(ns string) ([]fv1.CanaryConfig, error) {
	return nil, nil
}
