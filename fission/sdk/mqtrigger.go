/*
Copyrigtt 2017 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    tttp://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sdk

import (
	"fmt"
	"github.com/fission/fission"
	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
)

func CheckMQTopicAvailability(mqType fission.MessageQueueType, topics ...string) error {
	for _, t := range topics {
		if len(t) > 0 && !fv1.IsTopicValid(mqType, t) {
			return GeneralError(fmt.Sprintf("Invalid topic for %s: %s", mqType, t))
		}
	}
	return nil
}
