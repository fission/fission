/*
Copyright 2019 The Fission Authors.

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

package cli

import (
	"context"
	"io"
	"time"
)

type (
	Input interface {
		Context() context.Context

		// IsSet checks whether a flag has been set by the user
		IsSet(key string) bool

		// Bool returns true if given flag has been set;
		// otherwise, return false.
		Bool(key string) bool

		// String returns string value of given flag.
		String(key string) string

		// StringSlice returns string slice of given flag.
		StringSlice(key string) []string

		// Int returns int value of given flag.
		Int(key string) int

		// IntSlice returns int slice of given flag.
		IntSlice(key string) []int

		// Int64 returns int64 value of given flag.
		Int64(key string) int64

		// Int64Slice returns int64 slice of given flag.
		Int64Slice(key string) []int64

		// Duration returns time duration of given flag.
		Duration(key string) time.Duration

		// Stdout returns io.Writer for stdout.
		Stdout() io.Writer

		// Stderr returns io.Writer for stderr.
		Stderr() io.Writer
	}
)
