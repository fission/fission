// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package logdb

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestByTimestampSort(t *testing.T) {
	t.Parallel()
	base := time.Date(2021, 6, 1, 10, 0, 0, 0, time.UTC)
	mk := func(offset time.Duration, msg string) LogEntry {
		return LogEntry{Timestamp: base.Add(offset), Message: msg}
	}

	t.Run("ascending", func(t *testing.T) {
		t.Parallel()
		entries := []LogEntry{mk(2*time.Second, "c"), mk(0, "a"), mk(time.Second, "b")}
		sort.Sort(ByTimestamp(entries, false))
		assert.Equal(t, []string{"a", "b", "c"}, []string{entries[0].Message, entries[1].Message, entries[2].Message})
	})

	t.Run("descending", func(t *testing.T) {
		t.Parallel()
		entries := []LogEntry{mk(0, "a"), mk(2*time.Second, "c"), mk(time.Second, "b")}
		sort.Sort(ByTimestamp(entries, true))
		assert.Equal(t, []string{"c", "b", "a"}, []string{entries[0].Message, entries[1].Message, entries[2].Message})
	})

	t.Run("len and swap", func(t *testing.T) {
		t.Parallel()
		s := ByTimestamp([]LogEntry{mk(0, "a"), mk(time.Second, "b")}, false)
		assert.Equal(t, 2, s.Len())
		s.Swap(0, 1)
		assert.Equal(t, "b", s.entries[0].Message)
	})
}

func TestGetLogDB(t *testing.T) {
	t.Run("kubernetes", func(t *testing.T) {
		db, err := GetLogDB(KUBERNETES, t.Context(), LogDBOptions{})
		require.NoError(t, err)
		assert.IsType(t, kubernetesLogs{}, db)
	})

	t.Run("unknown type errors", func(t *testing.T) {
		_, err := GetLogDB("mysql", t.Context(), LogDBOptions{})
		require.Error(t, err)
	})
}

func TestLogDBRegistry(t *testing.T) {
	// Built-in drivers self-register via init().
	supported := supportedDrivers()
	assert.Contains(t, supported, KUBERNETES, "kubernetes driver must be registered")
	assert.Contains(t, supported, LOKI, "loki driver must be registered")

	// A registered driver is resolved by GetLogDB.
	Register("fake-test-driver", func(_ context.Context, _ LogDBOptions) (LogDatabase, error) {
		return kubernetesLogs{}, nil
	})
	db, err := GetLogDB("fake-test-driver", t.Context(), LogDBOptions{})
	require.NoError(t, err)
	assert.NotNil(t, db)

	// An unknown driver errors and names the supported drivers so the user can fix it.
	_, err = GetLogDB("mysql", t.Context(), LogDBOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), KUBERNETES)
}
