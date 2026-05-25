// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package validator

import (
	"sync"
)

var (
	topicValidators      = make(map[string]TopicValidator)
	lock                 = sync.Mutex{}
	kedaMqTypeValidators = map[string]bool{
		"kafka":              true,
		"aws-sqs-queue":      true,
		"aws-kinesis-stream": true,
		"gcp-pubsub":         true,
		"stan":               true,
		"rabbitmq":           true,
		"redis":              true,
		"nats-jetstream":     true,
	}
)

type (
	TopicValidator func(topic string) bool
)

func Register(mqType string, validator TopicValidator) {
	lock.Lock()
	defer lock.Unlock()

	if validator == nil {
		panic("Nil message queue topic validator")
	}

	_, registered := topicValidators[mqType]
	if registered {
		panic("Message queue topic validator already register")
	}

	topicValidators[mqType] = validator
}

func IsValidTopic(mqType, topic, mqtKind string) bool {
	if mqtKind == "keda" {
		return true
	}
	validator, registered := topicValidators[mqType]
	if !registered {
		return false
	}
	return validator(topic)
}

func IsValidMessageQueue(mqType, mqtKind string) bool {
	if mqtKind == "keda" {
		return kedaMqTypeValidators[mqType]
	}
	_, registered := topicValidators[mqType]
	return registered
}
