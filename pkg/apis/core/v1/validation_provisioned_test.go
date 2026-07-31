// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateProvisionedConcurrency(t *testing.T) {
	tests := []struct {
		name                   string
		provisionedConcurrency ProvisionedConcurrencyConfig
		expectedError          string
	}{
		{
			name: "valid",
			provisionedConcurrency: ProvisionedConcurrencyConfig{
				Target: 1,
				Windows: []ProvisionedWindow{
					{
						Name:     "default",
						Duration: "10m",
						Start:    "0 9 * * *",
						Target:   5,
					},
				},
			},
			expectedError: "",
		},
		{
			name: "valid: target zero",
			provisionedConcurrency: ProvisionedConcurrencyConfig{
				Target: 1,
				Windows: []ProvisionedWindow{
					{
						Name:     "default",
						Duration: "10m",
						Start:    "0 9 * * *",
						Target:   0,
					},
				},
			},
			expectedError: "",
		},
		{
			name: "invalid: target negative",
			provisionedConcurrency: ProvisionedConcurrencyConfig{
				Target: 1,
				Windows: []ProvisionedWindow{
					{
						Name:     "default",
						Duration: "10m",
						Start:    "0 9 * * *",
						Target:   -1,
					},
				},
			},
			expectedError: "window target must be >= 0",
		},
		{
			name: "invalid: duplicate window",
			provisionedConcurrency: ProvisionedConcurrencyConfig{
				Target: 1,
				Windows: []ProvisionedWindow{
					{
						Name:     "default",
						Duration: "10m",
						Start:    "0 9 * * *",
						Target:   5,
					},
					{
						Name:     "default",
						Duration: "10m",
						Start:    "0 9 * * *",
						Target:   5,
					},
				},
			},
			expectedError: "name must be unique",
		},
		{
			name: "invalid: name missing",
			provisionedConcurrency: ProvisionedConcurrencyConfig{
				Target: 1,
				Windows: []ProvisionedWindow{
					{
						Name:     "",
						Duration: "10m",
						Start:    "0 9 * * *",
						Target:   5,
					},
				},
			},
			expectedError: "name is required",
		},
		{
			name: "invalid: cron",
			provisionedConcurrency: ProvisionedConcurrencyConfig{
				Target: 1,
				Windows: []ProvisionedWindow{
					{
						Name:     "default",
						Duration: "10m",
						Start:    "invalid cron",
						Target:   5,
					},
				},
			},
			expectedError: "start is invalid: ",
		},
		{
			name: "invalid: duration",
			provisionedConcurrency: ProvisionedConcurrencyConfig{
				Target: 1,
				Windows: []ProvisionedWindow{
					{
						Name:     "default",
						Duration: "invalid duration",
						Start:    "0 9 * * *",
						Target:   5,
					},
				},
			},
			expectedError: "duration is invalid: ",
		},
		{
			name: "invalid: negative duration",
			provisionedConcurrency: ProvisionedConcurrencyConfig{
				Target: 1,
				Windows: []ProvisionedWindow{
					{
						Name:     "default",
						Duration: "-10s",
						Start:    "0 9 * * *",
						Target:   5,
					},
				},
			},
			expectedError: "[duration must be > 0 window name: default]",
		},
		{
			name: "invalid: zero duration",
			provisionedConcurrency: ProvisionedConcurrencyConfig{
				Target: 1,
				Windows: []ProvisionedWindow{
					{
						Name:     "default",
						Duration: "0s",
						Start:    "0 9 * * *",
						Target:   5,
					},
				},
			},
			expectedError: "[duration must be > 0 window name: default]",
		},
		{
			name: "invalid: zero target",
			provisionedConcurrency: ProvisionedConcurrencyConfig{
				Target: 0,
				Windows: []ProvisionedWindow{
					{
						Name:     "default",
						Duration: "10m",
						Start:    "0 9 * * *",
						Target:   5,
					},
				},
			},
			expectedError: "must be >= 1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.provisionedConcurrency.Validate()
			if test.expectedError == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), test.expectedError)
			}
		})
	}
}
