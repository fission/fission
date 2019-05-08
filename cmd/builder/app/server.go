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

package app

import (
	"net/http"

	"go.uber.org/zap"

	builder "github.com/fission/fission/pkg/builder"
)

// Usage: builder <shared volume path>
func Run(logger *zap.Logger, shareVolume string) error {
	builder := builder.MakeBuilder(logger, shareVolume)
	mux := http.NewServeMux()
	mux.HandleFunc("/", builder.Handler)
	mux.HandleFunc("/version", builder.VersionHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return http.ListenAndServe(":8001", mux)
}
