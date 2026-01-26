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

package mqtrigger

import (
	"errors"
	"fmt"
)

// Sentinel errors for the mqtrigger package.
// These can be checked with errors.Is() for specific error handling.
var (
	// ErrTriggerAlreadyExists indicates that a trigger subscription already exists.
	ErrTriggerAlreadyExists = errors.New("trigger already exists")

	// ErrTriggerNotFound indicates that a trigger subscription was not found.
	ErrTriggerNotFound = errors.New("trigger does not exist")

	// ErrTriggerSubscriptionNotFound indicates that a trigger subscription was not found.
	ErrTriggerSubscriptionNotFound = errors.New("trigger subscription does not exist")

	// ErrSubscriptionNil indicates that the subscription returned from the message queue is nil.
	ErrSubscriptionNil = errors.New("subscription is nil")

	// ErrListerNotFound indicates that no lister was found for the specified namespace.
	ErrListerNotFound = errors.New("no messagequeuetrigger lister found for namespace")
)

// ListerNotFoundError provides detailed error information when a lister is not found.
type ListerNotFoundError struct {
	Namespace string
}

func (e *ListerNotFoundError) Error() string {
	return fmt.Sprintf("%s: %s", ErrListerNotFound.Error(), e.Namespace)
}

func (e *ListerNotFoundError) Unwrap() error {
	return ErrListerNotFound
}

// NewListerNotFoundError creates a new ListerNotFoundError for the given namespace.
func NewListerNotFoundError(namespace string) error {
	return &ListerNotFoundError{Namespace: namespace}
}
