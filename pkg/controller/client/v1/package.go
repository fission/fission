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

package v1

import (
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/controller/client/rest"
)

type (
	PackageGetter interface {
		Package() PackageInterface
	}

	PackageInterface interface {
		Create(f *fv1.Package) (*metav1.ObjectMeta, error)
		Get(m *metav1.ObjectMeta) (*fv1.Package, error)
		Update(f *fv1.Package) (*metav1.ObjectMeta, error)
		Delete(m *metav1.ObjectMeta) error
		List(pkgNamespace string) ([]fv1.Package, error)
	}

	Package struct {
		client rest.Interface
	}
)

func newPackageClient(c *V1) PackageInterface {
	return &Package{client: c.restClient}
}

func (c *Package) Create(f *fv1.Package) (*metav1.ObjectMeta, error) {
	err := f.Validate()
	if err != nil {
		return nil, fv1.AggregateValidationErrors("Package", err)
	}

	reqbody, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Create("packages", "application/json", reqbody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := handleCreateResponse(resp)
	if err != nil {
		return nil, err
	}

	var m metav1.ObjectMeta
	err = json.Unmarshal(body, &m)
	if err != nil {
		return nil, err
	}

	return &m, nil
}

func (c *Package) Get(m *metav1.ObjectMeta) (*fv1.Package, error) {
	relativeUrl := fmt.Sprintf("packages/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)

	resp, err := c.client.Get(relativeUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := handleResponse(resp)
	if err != nil {
		return nil, err
	}

	var f fv1.Package
	err = json.Unmarshal(body, &f)
	if err != nil {
		return nil, err
	}

	return &f, nil
}

func (c *Package) Update(f *fv1.Package) (*metav1.ObjectMeta, error) {
	err := f.Validate()
	if err != nil {
		return nil, fv1.AggregateValidationErrors("Package", err)
	}

	reqbody, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	relativeUrl := fmt.Sprintf("packages/%v", f.ObjectMeta.Name)

	resp, err := c.client.Put(relativeUrl, "application/json", reqbody)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := handleResponse(resp)
	if err != nil {
		return nil, err
	}

	var m metav1.ObjectMeta
	err = json.Unmarshal(body, &m)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (c *Package) Delete(m *metav1.ObjectMeta) error {
	relativeUrl := fmt.Sprintf("packages/%v", m.Name)
	relativeUrl += fmt.Sprintf("?namespace=%v", m.Namespace)
	return c.client.Delete(relativeUrl)
}

func (c *Package) List(pkgNamespace string) ([]fv1.Package, error) {
	relativeUrl := fmt.Sprintf("packages?namespace=%v", pkgNamespace)
	resp, err := c.client.Get(relativeUrl)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := handleResponse(resp)
	if err != nil {
		return nil, err
	}

	funcs := make([]fv1.Package, 0)
	err = json.Unmarshal(body, &funcs)
	if err != nil {
		return nil, err
	}

	return funcs, nil
}
