// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package dummy is an in-memory cli.Input for unit tests: flags are set with
// typed setters and read back through the cli.Input getters, so a test that
// sets a flag with the wrong type fails to compile instead of panicking in
// the getter.
package dummy

import (
	"context"
	"io"
	"os"
	"time"

	fCli "github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
)

var _ fCli.Input = Cli{}

// Cli is a value type over shared maps, so the setters work on copies too.
type Cli struct {
	bools        map[string]bool
	strings      map[string]string
	stringSlices map[string][]string
	ints         map[string]int
	intSlices    map[string][]int
	int64s       map[string]int64
	int64Slices  map[string][]int64
	durations    map[string]time.Duration
}

// TestFlagSet returns an empty flag set for unit test purpose.
func TestFlagSet() Cli {
	return Cli{
		bools:        make(map[string]bool),
		strings:      make(map[string]string),
		stringSlices: make(map[string][]string),
		ints:         make(map[string]int),
		intSlices:    make(map[string][]int),
		int64s:       make(map[string]int64),
		int64Slices:  make(map[string][]int64),
		durations:    make(map[string]time.Duration),
	}
}

// Flag sets one flag on a Cli; build them with String, Bool, Int, ... so a
// table-driven test can list its flags as data.
type Flag func(Cli)

// TestFlagSetWith returns a flag set with the given flags applied.
func TestFlagSetWith(flags ...Flag) Cli {
	c := TestFlagSet()
	for _, f := range flags {
		f(c)
	}
	return c
}

func String(key, v string) Flag                 { return func(c Cli) { c.SetString(key, v) } }
func Bool(key string, v bool) Flag              { return func(c Cli) { c.SetBool(key, v) } }
func Int(key string, v int) Flag                { return func(c Cli) { c.SetInt(key, v) } }
func Int64(key string, v int64) Flag            { return func(c Cli) { c.SetInt64(key, v) } }
func StringSlice(key string, v []string) Flag   { return func(c Cli) { c.SetStringSlice(key, v) } }
func IntSlice(key string, v []int) Flag         { return func(c Cli) { c.SetIntSlice(key, v) } }
func Int64Slice(key string, v []int64) Flag     { return func(c Cli) { c.SetInt64Slice(key, v) } }
func Duration(key string, v time.Duration) Flag { return func(c Cli) { c.SetDuration(key, v) } }

func (u Cli) Context() context.Context {
	return context.TODO()
}

func (u Cli) SetBool(key string, v bool)              { u.bools[key] = v }
func (u Cli) SetString(key, v string)                 { u.strings[key] = v }
func (u Cli) SetStringSlice(key string, v []string)   { u.stringSlices[key] = v }
func (u Cli) SetInt(key string, v int)                { u.ints[key] = v }
func (u Cli) SetIntSlice(key string, v []int)         { u.intSlices[key] = v }
func (u Cli) SetInt64(key string, v int64)            { u.int64s[key] = v }
func (u Cli) SetInt64Slice(key string, v []int64)     { u.int64Slices[key] = v }
func (u Cli) SetDuration(key string, v time.Duration) { u.durations[key] = v }

func (u Cli) IsSet(key string) bool {
	if _, ok := u.bools[key]; ok {
		return true
	}
	if _, ok := u.strings[key]; ok {
		return true
	}
	if _, ok := u.stringSlices[key]; ok {
		return true
	}
	if _, ok := u.ints[key]; ok {
		return true
	}
	if _, ok := u.intSlices[key]; ok {
		return true
	}
	if _, ok := u.int64s[key]; ok {
		return true
	}
	if _, ok := u.int64Slices[key]; ok {
		return true
	}
	_, ok := u.durations[key]
	return ok
}

func (u Cli) Bool(key string) bool              { return u.bools[key] }
func (u Cli) String(key string) string          { return u.strings[key] }
func (u Cli) StringSlice(key string) []string   { return u.stringSlices[key] }
func (u Cli) Int(key string) int                { return u.ints[key] }
func (u Cli) IntSlice(key string) []int         { return u.intSlices[key] }
func (u Cli) Int64(key string) int64            { return u.int64s[key] }
func (u Cli) Int64Slice(key string) []int64     { return u.int64Slices[key] }
func (u Cli) Duration(key string) time.Duration { return u.durations[key] }

func (u Cli) Stdout() io.Writer {
	return os.Stdout
}

func (u Cli) Stderr() io.Writer {
	return os.Stderr
}
