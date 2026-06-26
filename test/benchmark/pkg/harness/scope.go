// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Scope is a per-scenario resource lifecycle: resources it creates register a
// cleanup, and Cleanup tears them down in reverse order. A scenario gets its own
// Scope so its resources are isolated from and cleaned independently of other
// scenarios sharing the Env.
type Scope struct {
	env   *Env
	label string

	mu       sync.Mutex
	cleanups []namedCleanup
}

type namedCleanup struct {
	name string
	fn   func(context.Context) error
}

// Env returns the shared run context (clients, capturer, router targets).
func (s *Scope) Env() *Env { return s.env }

// Name returns a scenario- and run-unique resource name with the given prefix,
// kept within DNS-1123 limits by the short prefixes/IDs callers use.
func (s *Scope) Name(prefix string) string {
	return fmt.Sprintf("%s-%s-%s", prefix, s.label, s.env.RunID)
}

func (s *Scope) addCleanup(name string, fn func(context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanups = append(s.cleanups, namedCleanup{name: name, fn: fn})
}

// CleanupDetached runs Cleanup on a context derived from ctx but immune to its
// cancellation (bounded by timeout), so teardown still proceeds when ctx has
// already expired — the common case when a scenario fails or times out.
func (s *Scope) CleanupDetached(ctx context.Context, timeout time.Duration) []error {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	return s.Cleanup(c)
}

// Cleanup runs all registered teardown actions in reverse order, always
// attempting every one, and returns any errors.
func (s *Scope) Cleanup(ctx context.Context) []error {
	s.mu.Lock()
	cleanups := s.cleanups
	s.cleanups = nil
	s.mu.Unlock()

	var errs []error
	for i := len(cleanups) - 1; i >= 0; i-- {
		if err := cleanups[i].fn(ctx); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", cleanups[i].name, err))
		}
	}
	return errs
}
