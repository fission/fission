// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	asv2 "k8s.io/api/autoscaling/v2"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util/hpa"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

func TestGetInvokeStrategy(t *testing.T) {
	cases := []struct {
		name                   string
		testArgs               map[string]any
		existingInvokeStrategy *fv1.InvokeStrategy
		expectedResult         *fv1.InvokeStrategy
		expectError            bool
	}{
		{
			name:                   "use default executor poolmgr",
			testArgs:               map[string]any{},
			existingInvokeStrategy: nil,
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypePoolmgr,
					SpecializationTimeout: 120,
				},
			},
			expectError: false,
		},
		{
			name:                   "executor type set to poolmgr",
			testArgs:               map[string]any{flagkey.FnExecutorType: string(fv1.ExecutorTypePoolmgr)},
			existingInvokeStrategy: nil,
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypePoolmgr,
					SpecializationTimeout: 120,
				},
			},
			expectError: false,
		},
		{
			name:                   "executor type set to newdeploy",
			testArgs:               map[string]any{flagkey.FnExecutorType: string(fv1.ExecutorTypeNewdeploy)},
			existingInvokeStrategy: nil,
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              DEFAULT_MIN_SCALE,
					MaxScale:              DEFAULT_MIN_SCALE,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			name:     "executor type change from poolmgr to newdeploy",
			testArgs: map[string]any{flagkey.FnExecutorType: string(fv1.ExecutorTypeNewdeploy)},
			existingInvokeStrategy: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType: fv1.ExecutorTypePoolmgr,
				},
			},
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              DEFAULT_MIN_SCALE,
					MaxScale:              DEFAULT_MIN_SCALE,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			name:     "executor type change from newdeploy to poolmgr",
			testArgs: map[string]any{flagkey.FnExecutorType: string(fv1.ExecutorTypePoolmgr)},
			existingInvokeStrategy: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              DEFAULT_MIN_SCALE,
					MaxScale:              DEFAULT_MIN_SCALE,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypePoolmgr,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			name: "minscale < maxscale",
			testArgs: map[string]any{
				flagkey.FnExecutorType:   string(fv1.ExecutorTypeNewdeploy),
				flagkey.ReplicasMinscale: 2,
				flagkey.ReplicasMaxscale: 3,
			},
			existingInvokeStrategy: nil,
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              2,
					MaxScale:              3,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			name: "minscale > maxscale",
			testArgs: map[string]any{
				flagkey.FnExecutorType:   string(fv1.ExecutorTypeNewdeploy),
				flagkey.ReplicasMinscale: 5,
				flagkey.ReplicasMaxscale: 3,
			},
			existingInvokeStrategy: nil,
			expectedResult:         nil,
			expectError:            true,
		},
		{
			name: "maxscale not specified",
			testArgs: map[string]any{
				flagkey.FnExecutorType:   string(fv1.ExecutorTypeNewdeploy),
				flagkey.ReplicasMinscale: 5,
			},
			existingInvokeStrategy: nil,
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              5,
					MaxScale:              5,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			name: "minscale not specified",
			testArgs: map[string]any{
				flagkey.FnExecutorType:   string(fv1.ExecutorTypeNewdeploy),
				flagkey.ReplicasMaxscale: 3,
			},
			existingInvokeStrategy: nil,
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              DEFAULT_MIN_SCALE,
					MaxScale:              3,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			name: "maxscale set to 0",
			testArgs: map[string]any{
				flagkey.FnExecutorType:   string(fv1.ExecutorTypeNewdeploy),
				flagkey.ReplicasMaxscale: 0,
			},
			existingInvokeStrategy: nil,
			expectedResult:         nil,
			expectError:            true,
		},
		{
			name: "update minscale with value larger than existing maxScale",
			testArgs: map[string]any{
				flagkey.FnExecutorType:   string(fv1.ExecutorTypeNewdeploy),
				flagkey.ReplicasMinscale: 9,
			},
			existingInvokeStrategy: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              2,
					MaxScale:              5,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectedResult: nil,
			expectError:    true,
		},
		{
			name: "maxscale set to 9 when existing is 5",
			testArgs: map[string]any{
				flagkey.FnExecutorType:   string(fv1.ExecutorTypeNewdeploy),
				flagkey.ReplicasMaxscale: 9,
			},
			existingInvokeStrategy: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              2,
					MaxScale:              5,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              2,
					MaxScale:              9,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			name: "change nothing for existing strategy",
			testArgs: map[string]any{
				flagkey.FnExecutorType: string(fv1.ExecutorTypeNewdeploy),
			},
			existingInvokeStrategy: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              2,
					MaxScale:              5,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              2,
					MaxScale:              5,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			name: "set target cpu percentage",
			testArgs: map[string]any{
				flagkey.FnExecutorType:   string(fv1.ExecutorTypeNewdeploy),
				flagkey.RuntimeTargetcpu: 50,
			},
			existingInvokeStrategy: nil,
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              DEFAULT_MIN_SCALE,
					MaxScale:              DEFAULT_MIN_SCALE,
					Metrics:               []asv2.MetricSpec{hpa.ConvertTargetCPUToCustomMetric(50)},
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			name: "change target cpu percentage",
			testArgs: map[string]any{
				flagkey.FnExecutorType:   string(fv1.ExecutorTypeNewdeploy),
				flagkey.RuntimeTargetcpu: 20,
			},
			existingInvokeStrategy: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              2,
					MaxScale:              5,
					Metrics:               []asv2.MetricSpec{hpa.ConvertTargetCPUToCustomMetric(88)},
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              2,
					MaxScale:              5,
					Metrics:               []asv2.MetricSpec{hpa.ConvertTargetCPUToCustomMetric(20)},
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			name: "change specializationtimeout",
			testArgs: map[string]any{
				flagkey.FnExecutorType:          string(fv1.ExecutorTypeNewdeploy),
				flagkey.FnSpecializationTimeout: 200,
			},
			existingInvokeStrategy: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType: fv1.ExecutorTypeNewdeploy,
					MinScale:     2,
					MaxScale:     5,
				},
			},
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              2,
					MaxScale:              5,
					SpecializationTimeout: 200,
				},
			},
			expectError: false,
		},
		{
			name: "specializationtimeout should not be less than 120",
			testArgs: map[string]any{
				flagkey.FnExecutorType:          string(fv1.ExecutorTypeNewdeploy),
				flagkey.FnSpecializationTimeout: 90,
			},
			existingInvokeStrategy: nil,
			expectedResult:         nil,
			expectError:            true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			flags := dummy.TestFlagSet()

			for k, v := range c.testArgs {
				flags.Set(k, v)
			}

			strategy, err := getInvokeStrategy(flags, c.existingInvokeStrategy)
			if c.expectError {
				require.NotNil(t, err)
				if err != nil {
					fmt.Println(err)
				}
			} else {
				require.Nil(t, err)
				if err == nil {
					require.NoError(t, strategy.Validate())
					require.Equal(t, *c.expectedResult, *strategy)
				}
			}
		})
	}
}

func TestGetProvisionedConcurrencyConfig(t *testing.T) {
	cases := []struct {
		name        string
		testArgs    map[string]any
		expected    *fv1.ProvisionedConcurrencyConfig
		errExpected bool
		errSubStrs  []string
	}{
		{
			name:     "flag not set returns nil",
			testArgs: map[string]any{},
			expected: nil,
		},
		{
			name:     "target positive builds config",
			testArgs: map[string]any{flagkey.FnProvisionedConcurrency: 5},
			expected: &fv1.ProvisionedConcurrencyConfig{Target: 5},
		},
		{
			name:     "target zero returns nil (off switch)",
			testArgs: map[string]any{flagkey.FnProvisionedConcurrency: 0},
			expected: nil,
		},
		{
			name:     "target negative returns nil",
			testArgs: map[string]any{flagkey.FnProvisionedConcurrency: -1},
			expected: nil,
		},
		{
			name:     "large target builds config",
			testArgs: map[string]any{flagkey.FnProvisionedConcurrency: 100},
			expected: &fv1.ProvisionedConcurrencyConfig{Target: 100},
		},
		{
			name:     "valid schedule",
			testArgs: map[string]any{flagkey.FnProvisionedConcurrency: 1, flagkey.FnProvisionedSchedule: []string{"name=w1;duration=8h;start=0 9 * * *;target=1"}},
			expected: &fv1.ProvisionedConcurrencyConfig{Target: 1, Windows: []fv1.ProvisionedWindow{{Name: "w1", Duration: "8h", Start: "0 9 * * *", Target: 1}}},
		},
		{
			name:        "valid schedule, invalid base target",
			testArgs:    map[string]any{flagkey.FnProvisionedConcurrency: 0, flagkey.FnProvisionedSchedule: []string{"name=w1;duration=8h;start=0 9 * * *;target=1"}},
			expected:    nil,
			errExpected: true,
			errSubStrs:  []string{"requires"},
		},
		{
			name:        "valid schedule, no concurrency",
			testArgs:    map[string]any{flagkey.FnProvisionedSchedule: []string{"name=w1;duration=8h;start=0 9 * * *;target=1"}},
			expected:    nil,
			errExpected: true,
			errSubStrs:  []string{"provisioned concurrency window is set but provisioned concurrency is not"},
		},
		{
			name:        "invalid schedule",
			testArgs:    map[string]any{flagkey.FnProvisionedConcurrency: 1, flagkey.FnProvisionedSchedule: []string{"name=w1;duration=8;start=0 9 * * *;target=1"}},
			errExpected: true,
			errSubStrs:  []string{"invalid duration"},
		},
		{
			name:        "duplicate window name",
			testArgs:    map[string]any{flagkey.FnProvisionedConcurrency: 1, flagkey.FnProvisionedSchedule: []string{"name=w1;duration=8h;start=0 9 * * *;target=1", "name=w1;duration=8h;start=0 9 * * *;target=1"}},
			errExpected: true,
			errSubStrs:  []string{"duplicate provisioned window: w1"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			flags := dummy.TestFlagSet()
			for k, v := range c.testArgs {
				flags.Set(k, v)
			}
			got, err := getProvisionedConcurrencyConfig(flags)
			if !c.errExpected {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				for _, substr := range c.errSubStrs {
					require.Contains(t, err.Error(), substr)
				}
			}
			require.Equal(t, c.expected, got)
		})
	}
}

func TestGetProvisioinedWindow(t *testing.T) {
	tests := []struct {
		name string // description of this test case
		// Named input parameters for target function.
		window    string
		want      fv1.ProvisionedWindow
		wantErr   bool
		errSubstr []string
	}{
		{
			name:   "valid window",
			window: "name=w1;start=0 9 * * *;duration=8h;target=3",
			want: fv1.ProvisionedWindow{
				Name:     "w1",
				Start:    "0 9 * * *",
				Duration: "8h",
				Target:   3,
			},
			wantErr: false,
		},
		{
			name:      "invalid window",
			window:    "",
			want:      fv1.ProvisionedWindow{},
			wantErr:   true,
			errSubstr: []string{"window details are empty"},
		},
		{
			name:      "missing target",
			window:    "name=w1;start=0 9 * * *;duration=8h",
			want:      fv1.ProvisionedWindow{},
			wantErr:   true,
			errSubstr: []string{"missing required window key: target"},
		},
		{
			name:      "missing name and target",
			window:    "start=0 9 * * *;duration=8h",
			want:      fv1.ProvisionedWindow{},
			wantErr:   true,
			errSubstr: []string{"missing required window key: name", "missing required window key: target"},
		},
		{
			name:      "unknown key",
			window:    "name=w1;start=0 9 * * *;duration=8h;target=3;unknown=1",
			want:      fv1.ProvisionedWindow{},
			wantErr:   true,
			errSubstr: []string{"invalid window definition: unknown is not a valid key"},
		},
		{
			name:      "malformed key",
			window:    "name=w1;start=0 9 * * *;duration=8h;target:3",
			want:      fv1.ProvisionedWindow{},
			wantErr:   true,
			errSubstr: []string{"invalid window definition: target:3, format is key=value"},
		},
		{
			name:      "negative target",
			window:    "name=w1;start=0 9 * * *;duration=8h;target=-1",
			want:      fv1.ProvisionedWindow{},
			wantErr:   true,
			errSubstr: []string{"target is negative"},
		},
		{
			name:      "bad target",
			window:    "name=w1;start=0 9 * * *;duration=8h;target=abc",
			want:      fv1.ProvisionedWindow{},
			wantErr:   true,
			errSubstr: []string{"invalid target:"},
		},
		{
			name:   "target zero",
			window: "name=w1;start=0 9 * * *;duration=8h;target=0",
			want: fv1.ProvisionedWindow{
				Name:     "w1",
				Start:    "0 9 * * *",
				Duration: "8h",
				Target:   0,
			},
			wantErr:   false,
			errSubstr: []string{},
		},
		{
			name:      "bad duration",
			window:    "name=w1;start=0 9 * * *;duration=abc;target=3",
			want:      fv1.ProvisionedWindow{},
			wantErr:   true,
			errSubstr: []string{"invalid duration:"},
		},
		{
			name:      "negative duration",
			window:    "name=w1;start=0 9 * * *;duration=-1h;target=3",
			want:      fv1.ProvisionedWindow{},
			wantErr:   true,
			errSubstr: []string{"duration is zero or negative"},
		},
		{
			name:      "bad start",
			window:    "name=w1;start=abc;duration=8h;target=3",
			want:      fv1.ProvisionedWindow{},
			wantErr:   true,
			errSubstr: []string{"invalid start:"},
		},
		{
			name:   "valid cron with comma splitting",
			window: "name=w1;start=0 9,13,17 * * *;duration=8h;target=3",
			want: fv1.ProvisionedWindow{
				Name:     "w1",
				Start:    "0 9,13,17 * * *",
				Duration: "8h",
				Target:   3,
			},
			wantErr:   false,
			errSubstr: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotErr := getProvisioinedWindow(tt.window)
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("getProvisioinedWindow() failed: %v", gotErr)
				}
				for _, substr := range tt.errSubstr {
					require.Contains(t, gotErr.Error(), substr)
				}
				return
			}
			if tt.wantErr {
				t.Fatal("getProvisioinedWindow() succeeded unexpectedly")
			}
			require.Equal(t, tt.want, got)
		})
	}
}
