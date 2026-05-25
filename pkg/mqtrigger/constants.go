// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqtrigger

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	// MaxRetries is the default maximum number of retries for queue operations.
	MaxRetries = 3

	// AuthTriggerSuffix is the suffix appended to trigger names to create authentication trigger names.
	AuthTriggerSuffix = "-auth-trigger"

	// MqtKindKeda represents the KEDA message queue trigger kind.
	MqtKindKeda = "keda"

	// MqtKindFission represents the Fission message queue trigger kind.
	MqtKindFission = "fission"

	// MqtAPIVersion is the API version for MessageQueueTrigger resources.
	MqtAPIVersion = "fission.io/v1"

	// MqtKind is the Kind for MessageQueueTrigger resources.
	MqtKind = "MessageQueueTrigger"

	// DefaultNumWorkers is the default number of workers for processing queues.
	DefaultNumWorkers = 4
)

// newOwnerReference creates an owner reference for a MessageQueueTrigger resource.
// This is used to establish ownership relationships between MQT and its child resources
// (Deployments, ScaledObjects, TriggerAuthentications).
func newOwnerReference(name string, uid types.UID) metav1.OwnerReference {
	blockOwnerDeletion := true
	return metav1.OwnerReference{
		Kind:               MqtKind,
		APIVersion:         MqtAPIVersion,
		Name:               name,
		UID:                uid,
		BlockOwnerDeletion: &blockOwnerDeletion,
	}
}

// authTriggerName generates the name for an authentication trigger based on the MQT name.
func authTriggerName(mqtName string) string {
	return mqtName + AuthTriggerSuffix
}
