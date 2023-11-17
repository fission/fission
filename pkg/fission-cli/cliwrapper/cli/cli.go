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

		//Parse(input interface{}) error

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

		// GlobalBool returns true if given global flag has been set;
		// otherwise, return false.
		GlobalBool(key string) bool

		// GlobalString returns global string value of given flag.
		GlobalString(key string) string

		// GlobalStringSlice returns global string slice of given flag.
		GlobalStringSlice(key string) []string

		// GlobalInt returns global int value of given flag.
		GlobalInt(key string) int

		// GlobalIntSlice returns global int slice of given flag.
		GlobalIntSlice(key string) []int

		// GlobalInt64 returns global int64 value of given flag.
		GlobalInt64(key string) int64

		// GlobalInt64Slice returns global int64 slice of given flag.
		GlobalInt64Slice(key string) []int64

		// Duration returns time duration of given flag.
		Duration(key string) time.Duration

		// OutOrStdout returns io.Writer for stdout.
		OutOrStdout() io.Writer

		// OutOrStderr returns io.Writer for stderr.
		OutOrStderr() io.Writer
	}
)
