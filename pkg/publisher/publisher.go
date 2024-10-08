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
