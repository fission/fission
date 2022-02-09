/*
Copyright 2020 The Fission Authors.

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

package validator

import (
	"sync"
)

var (
	topicValidators = make(map[string]TopicValidator)
	lock            = sync.Mutex{}
	kedaValidators  = map[string]bool{
		"kafka":              true,
		"aws-sqs-queue":      true,
		"aws-kinesis-stream": true,
		"gcp-pubsub":         true,
		"stan":               true,
		"rabbitmq":           true,
		"redis":              true,
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
		return kedaValidators[mqType]
	}
	_, registered := topicValidators[mqType]
	return registered
}
