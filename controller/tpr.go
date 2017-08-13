/*
Copyright 2016 The Fission Authors.

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

package controller

import (
	"errors"
	"regexp"

	"github.com/fission/fission/tpr"
)

func makeTPRBackedAPI() (*API, error) {
	fissionClient, _, err := tpr.MakeFissionClient()
	if err != nil {
		return nil, err
	}
	return &API{fissionClient: fissionClient}, nil
}

func validateResourceName(name string) error {
	re := regexp.MustCompile(`[a-z0-9]([-a-z0-9]*[a-z0-9])?`)
	if len(re.FindString(name)) != len(name) {
		return errors.New("Name must consist of lower case alphanumeric characters or '-', and must start and end with an alphanumeric character (e.g. 'my-name',  or '123-abc', regex used for validation is '[a-z0-9]([-a-z0-9]*[a-z0-9])?'")
	}
	return nil
}
