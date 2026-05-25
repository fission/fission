// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

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
