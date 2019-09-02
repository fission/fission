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

package urfavecli

import (
	"log"

	"github.com/urfave/cli"

	fCli "github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
)

var _ fCli.Input = &Cli{}

type Cli struct {
	c *cli.Context
}

// Parse is only for converting urfave *cli.Context to Input and will be removed in future.
func Parse(c *cli.Context) fCli.Input {
	return Cli{c: c}
}

func Wrapper(action cmd.CommandAction) func(*cli.Context) error {
	return func(c *cli.Context) error {
		e := action(Cli{c: c})
		// Urfave cli doesn't exit with error code even error is not nil.
		// We have to check whether error is empty and print error log here.
		if e != nil {
			log.Fatalf("%v", e)
		}
		return e
	}
}

func (u Cli) IsSet(key string) bool {
	return u.c.IsSet(key)
}

func (u Cli) Bool(key string) bool {
	return u.c.Bool(key)
}

func (u Cli) String(key string) string {
	return u.c.String(key)
}

func (u Cli) StringSlice(key string) []string {
	return u.c.StringSlice(key)
}

func (u Cli) Int(key string) int {
	return u.c.Int(key)
}

func (u Cli) IntSlice(key string) []int {
	return u.c.IntSlice(key)
}

func (u Cli) Int64(key string) int64 {
	return u.c.Int64(key)
}

func (u Cli) Int64Slice(key string) []int64 {
	return u.c.Int64Slice(key)
}

func (u Cli) GlobalBool(key string) bool {
	return u.c.GlobalBool(key)
}

func (u Cli) GlobalString(key string) string {
	return u.c.GlobalString(key)
}

func (u Cli) GlobalStringSlice(key string) []string {
	return u.c.GlobalStringSlice(key)
}

func (u Cli) GlobalInt(key string) int {
	return u.c.GlobalInt(key)
}

func (u Cli) GlobalIntSlice(key string) []int {
	return u.c.GlobalIntSlice(key)
}

func (u Cli) GlobalInt64(key string) int64 {
	return u.c.GlobalInt64(key)
}

func (u Cli) GlobalInt64Slice(key string) []int64 {
	return u.c.GlobalInt64Slice(key)
}
