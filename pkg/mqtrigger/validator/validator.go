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

var (
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

func IsValidTopic(mqtKind string) bool {
	return mqtKind == "keda"
}

func IsValidMessageQueue(mqType, mqtKind string) bool {
	if mqtKind == "keda" {
		return kedaMqTypeValidators[mqType]
	}
	return false
}
