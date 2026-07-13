// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
)

// defaultDriver is used when Config.Driver is empty.
const defaultDriver = "memory"

// Config selects and configures a driver set. It is read once at component start
// (via FromEnv or an explicit literal) and passed to Open. Fields beyond Driver
// are added as drivers land (Postgres DSN, Redis address, embedded-store URL).
type Config struct {
	// Driver names the registered driver to open. Empty means "memory".
	Driver string
}

// FromEnv builds a Config from the environment. This is the only place the
// package reads the environment; library constructors stay deterministic
// (Options-only), so unit tests inject a driver directly.
func FromEnv() Config {
	return Config{
		Driver: os.Getenv("STATESTORE_DRIVER"),
	}
}

// Constructor opens a driver's Capabilities from a Config.
type Constructor func(ctx context.Context, c Config) (Capabilities, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Constructor{}
)

// Register makes a driver available to Open under name. Drivers call this from an
// init function; the process (or a test) enables a driver with a blank import,
// e.g. import _ "github.com/fission/fission/pkg/statestore/memory". Register
// panics on a duplicate name or a nil constructor so misconfiguration fails
// loudly at startup.
func Register(name string, ctor Constructor) {
	if ctor == nil {
		panic("statestore: Register called with a nil constructor for " + name)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("statestore: driver already registered: " + name)
	}
	registry[name] = ctor
}

// Open returns the Capabilities for the configured driver. An empty Config.Driver
// selects the default ("memory"). An unregistered driver returns an error that
// names the available drivers.
func Open(ctx context.Context, c Config) (Capabilities, error) {
	name := c.Driver
	if name == "" {
		name = defaultDriver
	}
	registryMu.RLock()
	ctor, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("statestore: unknown driver %q (registered: %v)", name, registeredDrivers())
	}
	return ctor(ctx, c)
}

// registeredDrivers returns the sorted set of registered driver names, for error
// messages.
func registeredDrivers() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
