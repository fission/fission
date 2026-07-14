// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeCaps is a no-op Capabilities used to exercise the driver registry without
// importing a real driver (which would create an import cycle).
type fakeCaps struct{}

func (fakeCaps) KV() (KVStore, error)        { return nil, ErrCapabilityUnavailable }
func (fakeCaps) EventLog() (EventLog, error) { return nil, ErrCapabilityUnavailable }
func (fakeCaps) Queue() (Queue, error)       { return nil, ErrCapabilityUnavailable }
func (fakeCaps) Ping(context.Context) error  { return nil }
func (fakeCaps) Close() error                { return nil }

func TestRegistryOpenAndUnknown(t *testing.T) {
	name := "fake-" + t.Name()
	Register(name, func(context.Context, Config) (Capabilities, error) { return fakeCaps{}, nil })

	caps, err := Open(t.Context(), Config{Driver: name})
	require.NoError(t, err)
	require.NotNil(t, caps)

	_, err = Open(t.Context(), Config{Driver: "does-not-exist-" + t.Name()})
	require.Error(t, err)
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	name := "dup-" + t.Name()
	Register(name, func(context.Context, Config) (Capabilities, error) { return fakeCaps{}, nil })
	require.Panics(t, func() {
		Register(name, func(context.Context, Config) (Capabilities, error) { return fakeCaps{}, nil })
	})
}

func TestFromEnvDefaultsToEmptyDriver(t *testing.T) {
	t.Setenv("STATESTORE_DRIVER", "")
	require.Equal(t, "", FromEnv().Driver)
	t.Setenv("STATESTORE_DRIVER", "postgres")
	require.Equal(t, "postgres", FromEnv().Driver)
}
