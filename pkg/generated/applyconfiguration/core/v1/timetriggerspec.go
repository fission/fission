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

import (
	corev1 "github.com/fission/fission/pkg/apis/core/v1"
)

// TimeTriggerSpecApplyConfiguration represents an declarative configuration of the TimeTriggerSpec type for use
// with apply.
type TimeTriggerSpecApplyConfiguration struct {
	Cron                                 *string `json:"cron,omitempty"`
	*FunctionReferenceApplyConfiguration `json:"functionref,omitempty"`
	Method                               *string `json:"method,omitempty"`
	Subpath                              *string `json:"subpath,omitempty"`
}

// TimeTriggerSpecApplyConfiguration constructs an declarative configuration of the TimeTriggerSpec type for use with
// apply.
func TimeTriggerSpec() *TimeTriggerSpecApplyConfiguration {
	return &TimeTriggerSpecApplyConfiguration{}
}

// WithCron sets the Cron field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Cron field is set to the value of the last call.
func (b *TimeTriggerSpecApplyConfiguration) WithCron(value string) *TimeTriggerSpecApplyConfiguration {
	b.Cron = &value
	return b
}

// WithType sets the Type field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Type field is set to the value of the last call.
func (b *TimeTriggerSpecApplyConfiguration) WithType(value corev1.FunctionReferenceType) *TimeTriggerSpecApplyConfiguration {
	b.ensureFunctionReferenceApplyConfigurationExists()
	b.Type = &value
	return b
}

// WithName sets the Name field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Name field is set to the value of the last call.
func (b *TimeTriggerSpecApplyConfiguration) WithName(value string) *TimeTriggerSpecApplyConfiguration {
	b.ensureFunctionReferenceApplyConfigurationExists()
	b.Name = &value
	return b
}

// WithFunctionWeights puts the entries into the FunctionWeights field in the declarative configuration
// and returns the receiver, so that objects can be build by chaining "With" function invocations.
// If called multiple times, the entries provided by each call will be put on the FunctionWeights field,
// overwriting an existing map entries in FunctionWeights field with the same key.
func (b *TimeTriggerSpecApplyConfiguration) WithFunctionWeights(entries map[string]int) *TimeTriggerSpecApplyConfiguration {
	b.ensureFunctionReferenceApplyConfigurationExists()
	if b.FunctionWeights == nil && len(entries) > 0 {
		b.FunctionWeights = make(map[string]int, len(entries))
	}
	for k, v := range entries {
		b.FunctionWeights[k] = v
	}
	return b
}

func (b *TimeTriggerSpecApplyConfiguration) ensureFunctionReferenceApplyConfigurationExists() {
	if b.FunctionReferenceApplyConfiguration == nil {
		b.FunctionReferenceApplyConfiguration = &FunctionReferenceApplyConfiguration{}
	}
}

// WithMethod sets the Method field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Method field is set to the value of the last call.
func (b *TimeTriggerSpecApplyConfiguration) WithMethod(value string) *TimeTriggerSpecApplyConfiguration {
	b.Method = &value
	return b
}

// WithSubpath sets the Subpath field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Subpath field is set to the value of the last call.
func (b *TimeTriggerSpecApplyConfiguration) WithSubpath(value string) *TimeTriggerSpecApplyConfiguration {
	b.Subpath = &value
	return b
}
