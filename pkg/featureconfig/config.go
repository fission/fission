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

package featureconfig

import (
	"encoding/base64"
	"fmt"
	"os"

	"go.uber.org/zap"
	"sigs.k8s.io/yaml"
)

// GetFeatureConfig reads the configMap file and unmarshals the config into a feature config struct
func GetFeatureConfig(logger *zap.Logger) (*FeatureConfig, error) {
	featureConfig := &FeatureConfig{}

	// check if the file exists
	if _, err := os.Stat(FeatureConfigFile); os.IsNotExist(err) {
		logger.Warn("using empty feature config as file not found", zap.String("configPath", FeatureConfigFile))
		return featureConfig, nil
	}

	// read the file
	b64EncodedContent, err := os.ReadFile(FeatureConfigFile)
	if err != nil {
		return nil, fmt.Errorf("error reading YAML file %s: %v", FeatureConfigFile, err)
	}

	// b64 decode file
	yamlContent, err := base64.StdEncoding.DecodeString(string(b64EncodedContent))
	if err != nil {
		return nil, fmt.Errorf("error b64 decoding the config : %v", err)
	}

	// unmarshal into feature config
	err = yaml.Unmarshal(yamlContent, featureConfig)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling YAML config %v", err)
	}

	if featureConfig.AuthConfig.AuthUriPath == "" {
		featureConfig.AuthConfig.AuthUriPath = "/auth/login"
	}

	return featureConfig, err
}
