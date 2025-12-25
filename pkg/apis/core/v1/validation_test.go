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
			var errs error
			errs = errors.Join(tc.errs...)
			aggErr := AggregateValidationErrors("Environment", errs)
			snaps.MatchSnapshot(t, fmt.Sprint(aggErr))
		})
	}
}
