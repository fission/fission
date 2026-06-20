// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package featureconfig

import (
	"encoding/base64"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	"sigs.k8s.io/yaml"
)

// GetFeatureConfig reads the configMap file and unmarshals the config into a feature config struct
func GetFeatureConfig(logger logr.Logger) (*FeatureConfig, error) {
	featureConfig := &FeatureConfig{}

	// check if the file exists
	if _, err := os.Stat(FeatureConfigFile); os.IsNotExist(err) {
		logger.Info("using empty feature config as file not found", "configPath", FeatureConfigFile)
		return featureConfig, nil
	}

	// read the file
	b64EncodedContent, err := os.ReadFile(FeatureConfigFile)
	if err != nil {
		return nil, fmt.Errorf("error reading YAML file %s: %w", FeatureConfigFile, err)
	}

	// b64 decode file
	yamlContent, err := base64.StdEncoding.DecodeString(string(b64EncodedContent))
	if err != nil {
		return nil, fmt.Errorf("error b64 decoding the config : %w", err)
	}

	// unmarshal into feature config
	err = yaml.Unmarshal(yamlContent, featureConfig)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling YAML config %w", err)
	}

	if featureConfig.AuthConfig.AuthUriPath == "" {
		featureConfig.AuthConfig.AuthUriPath = "/auth/login"
	}

	return featureConfig, err
}
