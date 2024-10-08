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

// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1

// IngressConfigApplyConfiguration represents a declarative configuration of the IngressConfig type for use
// with apply.
type IngressConfigApplyConfiguration struct {
	Annotations map[string]string `json:"annotations,omitempty"`
	Path        *string           `json:"path,omitempty"`
	Host        *string           `json:"host,omitempty"`
	TLS         *string           `json:"tls,omitempty"`
}

// IngressConfigApplyConfiguration constructs a declarative configuration of the IngressConfig type for use with
// apply.
func IngressConfig() *IngressConfigApplyConfiguration {
	return &IngressConfigApplyConfiguration{}
}

// WithAnnotations puts the entries into the Annotations field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, the entries provided by each call will be put on the Annotations field,
// overwriting an existing map entries in Annotations field with the same key.
func (b *IngressConfigApplyConfiguration) WithAnnotations(entries map[string]string) *IngressConfigApplyConfiguration {
	if b.Annotations == nil && len(entries) > 0 {
		b.Annotations = make(map[string]string, len(entries))
	}
	for k, v := range entries {
		b.Annotations[k] = v
	}
	return b
}

// WithPath sets the Path field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Path field is set to the value of the last call.
func (b *IngressConfigApplyConfiguration) WithPath(value string) *IngressConfigApplyConfiguration {
	b.Path = &value
	return b
}

// WithHost sets the Host field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Host field is set to the value of the last call.
func (b *IngressConfigApplyConfiguration) WithHost(value string) *IngressConfigApplyConfiguration {
	b.Host = &value
	return b
}

// WithTLS sets the TLS field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the TLS field is set to the value of the last call.
func (b *IngressConfigApplyConfiguration) WithTLS(value string) *IngressConfigApplyConfiguration {
	b.TLS = &value
	return b
}
