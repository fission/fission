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
	"io"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/fission/fission/pkg/controller/client/v1"
	"github.com/fission/fission/pkg/info"
)

// TODO: we should remove this interface, having this for now is for backward compatibility.
type (
	FakeMisc struct{}
)

func newMiscClient(c *v1.V1) v1.MiscInterface {
	return &FakeMisc{}
}

func (c *FakeMisc) SecretExists(m *metav1.ObjectMeta) error {
	return nil
}

func (c *FakeMisc) ConfigMapExists(m *metav1.ObjectMeta) error {
	return nil
}

func (c *FakeMisc) GetSvcURL(label string) (string, error) {
	return "", nil
}

func (c *FakeMisc) ServerInfo() (*info.ServerInfo, error) {
	return &info.ServerInfo{}, nil
}

func (c *FakeMisc) PodLogs(m *metav1.ObjectMeta) (io.ReadCloser, int, error) {
	return nil, 0, nil
}
