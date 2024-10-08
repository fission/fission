/*
Copyright The Fission Authors.

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

// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	"context"
	json "encoding/json"
	"fmt"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	corev1 "github.com/fission/fission/pkg/generated/applyconfiguration/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	testing "k8s.io/client-go/testing"
)

// FakePackages implements PackageInterface
type FakePackages struct {
	Fake *FakeCoreV1
	ns   string
}

var packagesResource = v1.SchemeGroupVersion.WithResource("packages")

var packagesKind = v1.SchemeGroupVersion.WithKind("Package")

// Get takes name of the _package, and returns the corresponding package object, and an error if there is any.
func (c *FakePackages) Get(ctx context.Context, name string, options metav1.GetOptions) (result *v1.Package, err error) {
	emptyResult := &v1.Package{}
	obj, err := c.Fake.
		Invokes(testing.NewGetActionWithOptions(packagesResource, c.ns, name, options), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1.Package), err
}

// List takes label and field selectors, and returns the list of Packages that match those selectors.
func (c *FakePackages) List(ctx context.Context, opts metav1.ListOptions) (result *v1.PackageList, err error) {
	emptyResult := &v1.PackageList{}
	obj, err := c.Fake.
		Invokes(testing.NewListActionWithOptions(packagesResource, packagesKind, c.ns, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &v1.PackageList{ListMeta: obj.(*v1.PackageList).ListMeta}
	for _, item := range obj.(*v1.PackageList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested packages.
func (c *FakePackages) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return c.Fake.
		InvokesWatch(testing.NewWatchActionWithOptions(packagesResource, c.ns, opts))

}

// Create takes the representation of a _package and creates it.  Returns the server's representation of the package, and an error, if there is any.
func (c *FakePackages) Create(ctx context.Context, _package *v1.Package, opts metav1.CreateOptions) (result *v1.Package, err error) {
	emptyResult := &v1.Package{}
	obj, err := c.Fake.
		Invokes(testing.NewCreateActionWithOptions(packagesResource, c.ns, _package, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1.Package), err
}

// Update takes the representation of a _package and updates it. Returns the server's representation of the package, and an error, if there is any.
func (c *FakePackages) Update(ctx context.Context, _package *v1.Package, opts metav1.UpdateOptions) (result *v1.Package, err error) {
	emptyResult := &v1.Package{}
	obj, err := c.Fake.
		Invokes(testing.NewUpdateActionWithOptions(packagesResource, c.ns, _package, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1.Package), err
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
func (c *FakePackages) UpdateStatus(ctx context.Context, _package *v1.Package, opts metav1.UpdateOptions) (result *v1.Package, err error) {
	emptyResult := &v1.Package{}
	obj, err := c.Fake.
		Invokes(testing.NewUpdateSubresourceActionWithOptions(packagesResource, "status", c.ns, _package, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1.Package), err
}

// Delete takes name of the _package and deletes it. Returns an error if one occurs.
func (c *FakePackages) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	_, err := c.Fake.
		Invokes(testing.NewDeleteActionWithOptions(packagesResource, c.ns, name, opts), &v1.Package{})

	return err
}

// DeleteCollection deletes a collection of objects.
func (c *FakePackages) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	action := testing.NewDeleteCollectionActionWithOptions(packagesResource, c.ns, opts, listOpts)

	_, err := c.Fake.Invokes(action, &v1.PackageList{})
	return err
}

// Patch applies the patch and returns the patched package.
func (c *FakePackages) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (result *v1.Package, err error) {
	emptyResult := &v1.Package{}
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceActionWithOptions(packagesResource, c.ns, name, pt, data, opts, subresources...), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1.Package), err
}

// Apply takes the given apply declarative configuration, applies it and returns the applied package.
func (c *FakePackages) Apply(ctx context.Context, _package *corev1.PackageApplyConfiguration, opts metav1.ApplyOptions) (result *v1.Package, err error) {
	if _package == nil {
		return nil, fmt.Errorf("_package provided to Apply must not be nil")
	}
	data, err := json.Marshal(_package)
	if err != nil {
		return nil, err
	}
	name := _package.Name
	if name == nil {
		return nil, fmt.Errorf("_package.Name must be provided to Apply")
	}
	emptyResult := &v1.Package{}
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceActionWithOptions(packagesResource, c.ns, *name, types.ApplyPatchType, data, opts.ToPatchOptions()), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1.Package), err
}

// ApplyStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating ApplyStatus().
func (c *FakePackages) ApplyStatus(ctx context.Context, _package *corev1.PackageApplyConfiguration, opts metav1.ApplyOptions) (result *v1.Package, err error) {
	if _package == nil {
		return nil, fmt.Errorf("_package provided to Apply must not be nil")
	}
	data, err := json.Marshal(_package)
	if err != nil {
		return nil, err
	}
	name := _package.Name
	if name == nil {
		return nil, fmt.Errorf("_package.Name must be provided to Apply")
	}
	emptyResult := &v1.Package{}
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceActionWithOptions(packagesResource, c.ns, *name, types.ApplyPatchType, data, opts.ToPatchOptions(), "status"), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1.Package), err
}
