// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package publisher

import "context"

type (
	// Publisher interface wraps the Publish method that publishes an request
	// with given "body" and "headers" to given "target"
	Publisher interface {
		// Publish a request to a "target". Target's meaning depends on the
		// publisher: it's a URL in the case of a webhook publisher, or a queue
		// name in a queue-based publisher such as NATS.
		Publish(ctx context.Context, body string, headers map[string]string, method, target string)
	}
)
