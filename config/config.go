package config

import (
	"io/ioutil"
	"time"

	"gopkg.in/yaml.v2"
)

type Config struct {
	Router Router `yaml:"router,omitempty"`
}

type Router struct {
	DialTimeout  time.Duration `yaml:"dialtimeout,omitempty"`
	AliveTimeout time.Duration `yaml:"alivetimeout,omitempty"`
	MaxRetries   int           `yaml:"maxretries,omitempty"`
}

func LoadConfig(path string) (*Config, error) {
	cfg := &Config{}

	b, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	err = yaml.Unmarshal(b, cfg)
	if err != nil {
		return nil, err
	}

	return cfg, err
}
