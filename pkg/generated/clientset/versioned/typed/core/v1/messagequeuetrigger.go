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

package v1

import (
	context "context"

	corev1 "github.com/fission/fission/pkg/apis/core/v1"
	applyconfigurationcorev1 "github.com/fission/fission/pkg/generated/applyconfiguration/core/v1"
	scheme "github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	gentype "k8s.io/client-go/gentype"
)

// MessageQueueTriggersGetter has a method to return a MessageQueueTriggerInterface.
// A group's client should implement this interface.
type MessageQueueTriggersGetter interface {
	MessageQueueTriggers(namespace string) MessageQueueTriggerInterface
}

// MessageQueueTriggerInterface has methods to work with MessageQueueTrigger resources.
type MessageQueueTriggerInterface interface {
	Create(ctx context.Context, _messageQueueTrigger *corev1.MessageQueueTrigger, opts metav1.CreateOptions) (*corev1.MessageQueueTrigger, error)
	Update(ctx context.Context, _messageQueueTrigger *corev1.MessageQueueTrigger, opts metav1.UpdateOptions) (*corev1.MessageQueueTrigger, error)
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.MessageQueueTrigger, error)
	List(ctx context.Context, opts metav1.ListOptions) (*corev1.MessageQueueTriggerList, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (result *corev1.MessageQueueTrigger, err error)
	Apply(ctx context.Context, _messageQueueTrigger *applyconfigurationcorev1.MessageQueueTriggerApplyConfiguration, opts metav1.ApplyOptions) (result *corev1.MessageQueueTrigger, err error)
	MessageQueueTriggerExpansion
}

// messageQueueTriggers implements MessageQueueTriggerInterface
type messageQueueTriggers struct {
	*gentype.ClientWithListAndApply[*corev1.MessageQueueTrigger, *corev1.MessageQueueTriggerList, *applyconfigurationcorev1.MessageQueueTriggerApplyConfiguration]
}

// newMessageQueueTriggers returns a MessageQueueTriggers
func newMessageQueueTriggers(c *CoreV1Client, namespace string) *messageQueueTriggers {
	return &messageQueueTriggers{
		gentype.NewClientWithListAndApply[*corev1.MessageQueueTrigger, *corev1.MessageQueueTriggerList, *applyconfigurationcorev1.MessageQueueTriggerApplyConfiguration](
			"messagequeuetriggers",
			c.RESTClient(),
			scheme.ParameterCodec,
			namespace,
			func() *corev1.MessageQueueTrigger { return &corev1.MessageQueueTrigger{} },
			func() *corev1.MessageQueueTriggerList { return &corev1.MessageQueueTriggerList{} },
		),
	}
}
