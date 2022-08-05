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

package encoder

import (
	"encoding/json"

	"sigs.k8s.io/yaml"
)

type (
	EncodeCodec string

	Encoder interface {
		Marshal(v interface{}) ([]byte, error)
		Unmarshal(data []byte, v interface{}) error
	}

	JSONEncoder struct{}
	YAMLEncoder struct{}
)

var _ Encoder = DefaultJSONEncoder()

func DefaultJSONEncoder() Encoder {
	return JSONEncoder{}
}

func (encoder JSONEncoder) Marshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

func (encoder JSONEncoder) Unmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

var _ Encoder = DefaultYAMLEncoder()

func DefaultYAMLEncoder() Encoder {
	return YAMLEncoder{}
}

func (encoder YAMLEncoder) Marshal(v interface{}) ([]byte, error) {
	return yaml.Marshal(v)
}

func (encoder YAMLEncoder) Unmarshal(data []byte, v interface{}) error {
	return yaml.Unmarshal(data, v)
}
