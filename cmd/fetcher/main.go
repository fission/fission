/*
Copyright 2018 The Fission Authors.

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

package main

import (
	"github.com/fission/fission/cmd/fetcher/app"
	"github.com/fission/fission/pkg/utils/loggerfactory"
	"github.com/fission/fission/pkg/utils/profile"
	"github.com/fission/fission/pkg/utils/signals"
)

// Usage: fetcher <shared volume path>
func main() {
	logger := loggerfactory.GetLogger()
	defer logger.Sync()

	profile.ProfileIfEnabled(logger)

	ctx := signals.SetupSignalHandlerWithContext(logger)
	app.Run(ctx, logger)
}
