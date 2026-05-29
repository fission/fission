// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqtrigger

import (
	"errors"
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
)
