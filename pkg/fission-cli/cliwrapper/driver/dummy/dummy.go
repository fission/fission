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
	c map[string]interface{}
}

// TestFlagSet returns a flag set for unit test purpose.
func TestFlagSet() Cli {
	return Cli{c: make(map[string]interface{})}
}

func (u Cli) Context() context.Context {
	return context.TODO()
}

// Set allows to set any kinds of value with given key.
// The type of set value should be matched with the returned
// type of GetXXX function.
func (u Cli) Set(Key string, value interface{}) {
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

func (u Cli) GlobalBool(key string) bool {
	val, ok := u.c[key]
	if !ok || val == nil {
		return false
	}
	return val.(bool)
}

func (u Cli) GlobalString(key string) string {
	val, ok := u.c[key]
	if !ok || val == nil {
		return ""
	}
	return val.(string)
}

func (u Cli) GlobalStringSlice(key string) []string {
	val, ok := u.c[key]
	if !ok || val == nil {
		return nil
	}
	return val.([]string)
}

func (u Cli) GlobalInt(key string) int {
	val, ok := u.c[key]
	if !ok || val == nil {
		return 0
	}
	return val.(int)
}

func (u Cli) GlobalIntSlice(key string) []int {
	val, ok := u.c[key]
	if !ok || val == nil {
		return nil
	}
	return val.([]int)
}

func (u Cli) GlobalInt64(key string) int64 {
	val, ok := u.c[key]
	if !ok || val == nil {
		return 0
	}
	return val.(int64)
}

func (u Cli) GlobalInt64Slice(key string) []int64 {
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
