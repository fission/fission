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
	asv2 "k8s.io/api/autoscaling/v2"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/executor/util/hpa"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

func TestGetInvokeStrategy(t *testing.T) {
	cases := []struct {
		name                   string
		testArgs               map[string]interface{}
		existingInvokeStrategy *fv1.InvokeStrategy
		expectedResult         *fv1.InvokeStrategy
		expectError            bool
	}{
		{
			name:                   "use default executor poolmgr",
			testArgs:               map[string]interface{}{},
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
			testArgs:               map[string]interface{}{flagkey.FnExecutorType: string(fv1.ExecutorTypePoolmgr)},
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
			testArgs:               map[string]interface{}{flagkey.FnExecutorType: string(fv1.ExecutorTypeNewdeploy)},
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
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			name:     "executor type change from newdeploy to poolmgr",
			testArgs: map[string]interface{}{flagkey.FnExecutorType: string(fv1.ExecutorTypePoolmgr)},
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
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			name: "minscale > maxscale",
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
			name: "maxscale not specified",
			testArgs: map[string]interface{}{
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
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			name: "maxscale set to 0",
			testArgs: map[string]interface{}{
				flagkey.FnExecutorType:   string(fv1.ExecutorTypeNewdeploy),
				flagkey.ReplicasMaxscale: 0,
			},
			existingInvokeStrategy: nil,
			expectedResult:         nil,
			expectError:            true,
		},
		{
			name: "update minscale with value larger than existing maxScale",
			testArgs: map[string]interface{}{
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
			testArgs: map[string]interface{}{
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
					Metrics:               []asv2.MetricSpec{hpa.ConvertTargetCPUToCustomMetric(50)},
					SpecializationTimeout: fv1.DefaultSpecializationTimeOut,
				},
			},
			expectError: false,
		},
		{
			name: "change target cpu percentage",
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
			testArgs: map[string]interface{}{
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
			testArgs: map[string]interface{}{
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
				assert.NotNil(t, err)
				if err != nil {
					fmt.Println(err)
				}
			} else {
				assert.Nil(t, err)
				if err == nil {
					assert.NoError(t, strategy.Validate())
					assert.Equal(t, *c.expectedResult, *strategy)
				}
			}
		})
	}
}
