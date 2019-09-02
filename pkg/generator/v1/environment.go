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

package v1

import (
	"github.com/pkg/errors"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/generator"
	"github.com/fission/fission/pkg/generator/encoder"
)

// ensure environment generator fits the generator interface
var _ generator.StructuredGenerator = &EnvironmentGenerator{}

type (
	EnvironmentGenerator struct {
		obj *fv1.Environment
	}
)

// CreateEnvironmentGeneratorFromObj creates environment generator initialized with pass-in environment object.
func CreateEnvironmentGeneratorFromObj(env *fv1.Environment) (*EnvironmentGenerator, error) {
	if env == nil {
		return nil, errors.New("cannot create environment generator with nil pointer")
	}

	if len(env.TypeMeta.Kind) == 0 {
		env.TypeMeta.Kind = fv1.CRD_NAME_ENVIRONMENT
	}

	if len(env.TypeMeta.APIVersion) == 0 {
		env.TypeMeta.APIVersion = fv1.CRD_VERSION
	}

	err := env.Validate()
	if err != nil {
		return nil, fv1.AggregateValidationErrors("Environment", err)
	}

	return &EnvironmentGenerator{
		obj: env,
	}, nil
}

func (generator EnvironmentGenerator) StructuredGenerate(enc encoder.Encoder) ([]byte, error) {
	return enc.Marshal(generator.obj)
}
