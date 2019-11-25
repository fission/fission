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

package function

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

func TestGetInvokeStrategy(t *testing.T) {
	cases := []struct {
		testArgs               map[string]interface{}
		existingInvokeStrategy *fv1.InvokeStrategy
		expectedResult         *fv1.InvokeStrategy
		expectError            bool
	}{
		{
			// case: use default executor poolmgr
			testArgs:               map[string]interface{}{},
			existingInvokeStrategy: nil,
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType: fv1.ExecutorTypePoolmgr,
				},
			},
			expectError: false,
		},
		{
			// case: executor type set to poolmgr
			testArgs:               map[string]interface{}{flagkey.FnExecutorType: string(fv1.ExecutorTypePoolmgr)},
			existingInvokeStrategy: nil,
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType: fv1.ExecutorTypePoolmgr,
				},
			},
			expectError: false,
		},
		{
			// case: executor type set to newdeploy
			testArgs:               map[string]interface{}{flagkey.FnExecutorType: string(fv1.ExecutorTypeNewdeploy)},
			existingInvokeStrategy: nil,
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              DEFAULT_MIN_SCALE,
					MaxScale:              DEFAULT_MIN_SCALE,
					TargetCPUPercent:      DEFAULT_TARGET_CPU_PERCENTAGE,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			// case: executor type change from poolmgr to newdeploy
			testArgs: map[string]interface{}{flagkey.FnExecutorType: string(fv1.ExecutorTypeNewdeploy)},
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
					TargetCPUPercent:      DEFAULT_TARGET_CPU_PERCENTAGE,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			// case: executor type change from newdeploy to poolmgr
			testArgs: map[string]interface{}{flagkey.FnExecutorType: string(fv1.ExecutorTypePoolmgr)},
			existingInvokeStrategy: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              DEFAULT_MIN_SCALE,
					MaxScale:              DEFAULT_MIN_SCALE,
					TargetCPUPercent:      DEFAULT_TARGET_CPU_PERCENTAGE,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType: fv1.ExecutorTypePoolmgr,
				},
			},
			expectError: false,
		},
		{
			// case: minscale < maxscale
			testArgs: map[string]interface{}{
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
					TargetCPUPercent:      DEFAULT_TARGET_CPU_PERCENTAGE,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			// case: minscale > maxscale
			testArgs: map[string]interface{}{
				flagkey.FnExecutorType:   string(fv1.ExecutorTypeNewdeploy),
				flagkey.ReplicasMinscale: 5,
				flagkey.ReplicasMaxscale: 3,
			},
			existingInvokeStrategy: nil,
			expectedResult:         nil,
			expectError:            true,
		},
		{
			// case: maxscale not specified
			testArgs: map[string]interface{}{
				flagkey.FnExecutorType:   string(fv1.ExecutorTypeNewdeploy),
				flagkey.ReplicasMinscale: 5,
			},
			existingInvokeStrategy: nil,
			expectedResult:         nil,
			expectError:            true,
		},
		{
			// case: minscale not specified
			testArgs: map[string]interface{}{
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
					TargetCPUPercent:      DEFAULT_TARGET_CPU_PERCENTAGE,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			// case: maxscale set to 0
			testArgs: map[string]interface{}{
				flagkey.FnExecutorType:   string(fv1.ExecutorTypeNewdeploy),
				flagkey.ReplicasMaxscale: 0,
			},
			existingInvokeStrategy: nil,
			expectedResult:         nil,
			expectError:            true,
		},
		{
			// case: maxscale set to 9 when existing is 5
			testArgs: map[string]interface{}{
				flagkey.FnExecutorType:   string(fv1.ExecutorTypeNewdeploy),
				flagkey.ReplicasMaxscale: 9,
			},
			existingInvokeStrategy: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              2,
					MaxScale:              5,
					TargetCPUPercent:      DEFAULT_TARGET_CPU_PERCENTAGE,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              2,
					MaxScale:              9,
					TargetCPUPercent:      DEFAULT_TARGET_CPU_PERCENTAGE,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			// case: change nothing for existing strategy
			testArgs: map[string]interface{}{
				flagkey.FnExecutorType: string(fv1.ExecutorTypeNewdeploy),
			},
			existingInvokeStrategy: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              2,
					MaxScale:              5,
					TargetCPUPercent:      DEFAULT_TARGET_CPU_PERCENTAGE,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              2,
					MaxScale:              5,
					TargetCPUPercent:      DEFAULT_TARGET_CPU_PERCENTAGE,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			// case: set target cpu percentage
			testArgs: map[string]interface{}{
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
					TargetCPUPercent:      50,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			// case: change target cpu percentage
			testArgs: map[string]interface{}{
				flagkey.FnExecutorType:   string(fv1.ExecutorTypeNewdeploy),
				flagkey.RuntimeTargetcpu: 20,
			},
			existingInvokeStrategy: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              2,
					MaxScale:              5,
					TargetCPUPercent:      88,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              2,
					MaxScale:              5,
					TargetCPUPercent:      20,
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			// case: change specializationtimeout
			testArgs: map[string]interface{}{
				flagkey.FnExecutorType:          string(fv1.ExecutorTypeNewdeploy),
				flagkey.FnSpecializationTimeout: 200,
			},
			existingInvokeStrategy: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:     fv1.ExecutorTypeNewdeploy,
					MinScale:         2,
					MaxScale:         5,
					TargetCPUPercent: DEFAULT_TARGET_CPU_PERCENTAGE,
				},
			},
			expectedResult: &fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType:          fv1.ExecutorTypeNewdeploy,
					MinScale:              2,
					MaxScale:              5,
					SpecializationTimeout: 200,
					TargetCPUPercent:      DEFAULT_TARGET_CPU_PERCENTAGE,
				},
			},
			expectError: false,
		},
		{
			// case: specializationtimeout should not work for poolmgr
			testArgs: map[string]interface{}{
				flagkey.FnExecutorType:          string(fv1.ExecutorTypePoolmgr),
				flagkey.FnSpecializationTimeout: 10,
			},
			existingInvokeStrategy: nil,
			expectedResult:         nil,
			expectError:            true,
		},
		{
			// case: specializationtimeout should not be less than 120
			testArgs: map[string]interface{}{
				flagkey.FnExecutorType:          string(fv1.ExecutorTypeNewdeploy),
				flagkey.FnSpecializationTimeout: 90,
			},
			existingInvokeStrategy: nil,
			expectedResult:         nil,
			expectError:            true,
		},
	}

	for i, c := range cases {
		fmt.Printf("=== Test Case %v ===\n", i)

		flags := dummy.TestFlagSet()

		for k, v := range c.testArgs {
			flags.Set(k, v)
		}

		strategy, err := getInvokeStrategy(flags, c.existingInvokeStrategy)
		if c.expectError {
			assert.NotNil(t, err)
			if err != nil {
				fmt.Println(err)
			}
		} else {
			assert.Nil(t, err)
			assert.NoError(t, strategy.Validate(), fmt.Sprintf("Failed at test case %v", i))
			assert.Equal(t, *c.expectedResult, *strategy)
		}
	}
}
