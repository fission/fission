// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package featureconfig

import "time"

const (
	FeatureConfigFile = "/etc/config/config.yaml"
	CanaryFeature     = "canary"
)

type (
	// config.yaml contains config parameters for optional features
	// To add new features with config parameters:
	// 1. create a yaml block with feature name in charts/_helpers.tpl
	// 2. define a corresponding struct with the feature config for the yaml unmarshal below
	// 3. start the appropriate controllers needed for this feature

	FeatureConfig struct {
		// In the future more such feature configs can be added here for each optional feature
		CanaryConfig CanaryFeatureConfig `json:"canary"`
		AuthConfig   AuthFeatureConfig   `json:"auth"`
	}

	// specific feature config
	CanaryFeatureConfig struct {
		IsEnabled     bool   `json:"enabled"`
		PrometheusSvc string `json:"prometheusSvc"`
	}

	AuthFeatureConfig struct {
		IsEnabled     bool          `json:"enabled"`
		AuthUriPath   string        `json:"authUriPath"`
		JWTExpiryTime time.Duration `json:"jwtExpiryTime"`
		JWTIssuer     string        `json:"jwtIssuer"`
	}
)
