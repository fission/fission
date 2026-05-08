/*
Copyright 2026 The Fission Authors.

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

package webhook

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// validBaseMQT returns a MessageQueueTrigger that satisfies the existing
// validation rules so we can isolate the new podSpec-allowlist check.
func validBaseMQT() *fv1.MessageQueueTrigger {
	return &fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: "mqt-1", Namespace: "default"},
		Spec: fv1.MessageQueueTriggerSpec{
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: "fn",
			},
			MessageQueueType: "kafka",
			Topic:            "events",
			MqtKind:          "keda",
		},
	}
}

func TestMessageQueueTriggerWebhookRejectsImageOverride(t *testing.T) {
	v := &MessageQueueTrigger{}
	mqt := validBaseMQT()
	mqt.Spec.PodSpec = &apiv1.PodSpec{
		Containers: []apiv1.Container{{Name: "connector", Image: "evil:latest"}},
	}

	err := v.Validate(mqt)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "podSpec.containers[].image")
}

func TestMessageQueueTriggerWebhookRejectsCommandAndArgs(t *testing.T) {
	v := &MessageQueueTrigger{}
	mqt := validBaseMQT()
	mqt.Spec.PodSpec = &apiv1.PodSpec{
		Containers: []apiv1.Container{{
			Name:    "connector",
			Command: []string{"/bin/sh"},
			Args:    []string{"-c", "curl evil"},
		}},
	}
	err := v.Validate(mqt)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "podSpec.containers[].command")
	assert.Contains(t, err.Error(), "podSpec.containers[].args")
}

func TestMessageQueueTriggerWebhookRejectsHostNamespacesAndSA(t *testing.T) {
	v := &MessageQueueTrigger{}
	mqt := validBaseMQT()
	mqt.Spec.PodSpec = &apiv1.PodSpec{
		Containers:         []apiv1.Container{{Name: "connector"}},
		ServiceAccountName: "cluster-admin",
		HostNetwork:        true,
	}
	err := v.Validate(mqt)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "podSpec.serviceAccountName")
	assert.Contains(t, err.Error(), "podSpec.hostNetwork")
}

func TestMessageQueueTriggerWebhookAcceptsAllowlistedPodSpec(t *testing.T) {
	v := &MessageQueueTrigger{}
	mqt := validBaseMQT()
	mqt.Spec.PodSpec = &apiv1.PodSpec{
		Containers: []apiv1.Container{{Name: "connector"}},
		NodeSelector: map[string]string{
			"role": "mqt",
		},
		Tolerations: []apiv1.Toleration{{
			Key:      "dedicated",
			Operator: apiv1.TolerationOpEqual,
			Value:    "mqt",
		}},
	}
	require.NoError(t, v.Validate(mqt))
}

func TestMessageQueueTriggerWebhookNilPodSpec(t *testing.T) {
	v := &MessageQueueTrigger{}
	mqt := validBaseMQT()
	require.NoError(t, v.Validate(mqt))
}
