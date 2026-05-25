// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package messageQueue

import (
	"context"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// Subscription represents an active subscription to a message queue.
// It provides methods to stop the subscription and check its status.
type Subscription interface {
	// Stop gracefully stops the subscription and releases resources.
	// It should be safe to call multiple times.
	Stop() error

	// Done returns a channel that is closed when the subscription is stopped.
	Done() <-chan struct{}
}

// Config holds the configuration for connecting to a message queue.
type Config struct {
	MQType  string
	Url     string
	Secrets map[string][]byte
}

// MessageQueue defines the interface for message queue implementations.
// Implementations must be safe for concurrent use.
type MessageQueue interface {
	// Subscribe creates a new subscription for the given trigger.
	// The provided context controls the lifetime of the subscription.
	// When the context is cancelled, the subscription should be stopped.
	Subscribe(ctx context.Context, trigger *fv1.MessageQueueTrigger) (Subscription, error)

	// Unsubscribe stops an active subscription and releases its resources.
	// It is safe to call even if the subscription has already been stopped.
	Unsubscribe(sub Subscription) error
}
