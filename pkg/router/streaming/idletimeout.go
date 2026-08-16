// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package streaming

import (
	"fmt"
	"time"
)

// ParseIdleTimeout validates an operator-supplied idle-window override (the
// value of an env var like ROUTER_STREAM_IDLE_TIMEOUT; the caller reads the
// env and passes the name for the error message). One implementation shared
// by the router and the MCP server so the same env var can never validate
// differently per consumer. A non-positive window would silently disable the
// idle watchdog (streams with no max-duration ceiling could then hang
// forever), so it is rejected at startup rather than failing open.
func ParseIdleTimeout(envName, value string) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("failed to parse stream idle timeout value('%s') from '%s': %w", value, envName, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("'%s' must be a positive duration, got %q", envName, value)
	}
	return d, nil
}
