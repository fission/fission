// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package dummy

import (
	"context"
	"io"
	"os"
	"time"

	fCli "github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
)

var _ fCli.Input = &Cli{}

type Cli struct {
	c map[string]any
}

// TestFlagSet returns a flag set for unit test purpose.
func TestFlagSet() Cli {
	return Cli{c: make(map[string]any)}
}

func (u Cli) Context() context.Context {
	return context.TODO()
}

// Set allows to set any kinds of value with given key.
// The type of set value should be matched with the returned
// type of GetXXX function.
func (u Cli) Set(Key string, value any) {
	u.c[Key] = value
}

func (u Cli) IsSet(key string) bool {
	_, ok := u.c[key]
	return ok
}

func (u Cli) Bool(key string) bool {
	val, ok := u.c[key]
	if !ok || val == nil {
		return false
	}
	return val.(bool)
}

func (u Cli) String(key string) string {
	val, ok := u.c[key]
	if !ok || val == nil {
		return ""
	}
	return val.(string)
}

func (u Cli) StringSlice(key string) []string {
	val, ok := u.c[key]
	if !ok || val == nil {
		return nil
	}
	return val.([]string)
}

func (u Cli) Int(key string) int {
	val, ok := u.c[key]
	if !ok || val == nil {
		return 0
	}
	return val.(int)
}

func (u Cli) IntSlice(key string) []int {
	val, ok := u.c[key]
	if !ok || val == nil {
		return nil
	}
	return val.([]int)
}

func (u Cli) Int64(key string) int64 {
	val, ok := u.c[key]
	if !ok || val == nil {
		return 0
	}
	return val.(int64)
}

func (u Cli) Int64Slice(key string) []int64 {
	val, ok := u.c[key]
	if !ok || val == nil {
		return nil
	}
	return val.([]int64)
}

func (u Cli) Duration(key string) time.Duration {
	val, ok := u.c[key]
	if !ok || val == nil {
		return 0
	}
	return val.(time.Duration)
}

func (u Cli) Stdout() io.Writer {
	return os.Stdout
}

func (u Cli) Stderr() io.Writer {
	return os.Stderr
}
