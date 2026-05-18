/*
Copyright 2018 The Fission Authors.

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

package v1

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gkampitakis/go-snaps/snaps"
)

func TestAggregateValidationErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		errs []error
	}{
		{
			name: "no errors",
			errs: []error{},
		},
		{
			name: "one error",
			errs: []error{
				fmt.Errorf("E1"),
			},
		},
		{
			name: "multiple errors",
			errs: []error{
				fmt.Errorf("E1"),
				fmt.Errorf("E2"),
				fmt.Errorf("E3"),
			},
		},
		{
			name: "nested errors",
			errs: []error{
				fmt.Errorf("E1"),
				errors.Join(
					fmt.Errorf("E2"),
					errors.Join(
						fmt.Errorf("E3"),
						fmt.Errorf("E4"),
					),
				),
				fmt.Errorf("E5"),
				errors.Join(
					fmt.Errorf("E6"),
					fmt.Errorf("E7"),
				),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := errors.Join(tc.errs...)
			aggErr := AggregateValidationErrors("Environment", errs)
			snaps.MatchSnapshot(t, fmt.Sprint(aggErr))
		})
	}

	t.Run("nil error", func(t *testing.T) {
		aggErr := AggregateValidationErrors("Environment", nil)
		snaps.MatchSnapshot(t, fmt.Sprint(aggErr))
	})

	t.Run("simple error", func(t *testing.T) {
		aggErr := AggregateValidationErrors("Environment", fmt.Errorf("simple error"))
		snaps.MatchSnapshot(t, fmt.Sprint(aggErr))
	})
}

func TestHTTPTriggerCorsConfig_Validate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     *HTTPTriggerCorsConfig
		wantErr bool
		errSub  string
	}{
		{
			name: "nil receiver is no-op",
			cfg:  nil,
		},
		{
			name: "valid exact-origin allowlist",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins: []string{"https://app.example.com"},
				AllowMethods: []string{"GET", "POST"},
			},
		},
		{
			name: "valid wildcard without credentials",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins: []string{"*"},
			},
		},
		{
			name: "wildcard with credentials rejected",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins:     []string{"*"},
				AllowCredentials: true,
			},
			wantErr: true,
			errSub:  "AllowCredentials=true",
		},
		{
			name: "missing scheme rejected",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins: []string{"app.example.com"},
			},
			wantErr: true,
			errSub:  "scheme and host",
		},
		{
			name: "origin with path rejected",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins: []string{"https://app.example.com/api"},
			},
			wantErr: true,
			errSub:  "path, query, fragment",
		},
		{
			name: "origin with query rejected",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins: []string{"https://app.example.com/?x=1"},
			},
			wantErr: true,
			errSub:  "path, query, fragment",
		},
		{
			name: "malformed MaxAge rejected",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins: []string{"https://app.example.com"},
				MaxAge:       "garbage",
			},
			wantErr: true,
			errSub:  "time.Duration",
		},
		{
			name: "negative MaxAge rejected",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins: []string{"https://app.example.com"},
				MaxAge:       "-5m",
			},
			wantErr: true,
			errSub:  "non-negative",
		},
		{
			name: "valid MaxAge accepted",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins: []string{"https://app.example.com"},
				MaxAge:       "10m",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errSub)
				}
				if tc.errSub != "" && !strings.Contains(err.Error(), tc.errSub) {
					t.Fatalf("error %q does not contain %q", err, tc.errSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
