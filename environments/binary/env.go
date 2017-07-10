package main

import (
	"fmt"
	"strings"
)

// Utility functions for working with environment variables
type Env struct {
	Vars []*EnvVar
}

type EnvVar struct {
	Key string
	Val string
}

func FromString(rawEnvVar string) *EnvVar {
	parts := strings.SplitN(rawEnvVar, "=", 2)
	return &EnvVar{parts[0], parts[1]}
}

func (ev *EnvVar) ToString() string {
	return fmt.Sprintf("%s=%s", ev.Key, ev.Val)
}

func (e *Env) SetEnv(envVar *EnvVar) {
	e.Vars = append(e.Vars, envVar)
}

func (e *Env) ToStringEnv() []string {
	var result []string
	for _, envVar := range e.Vars {
		result = append(result, envVar.ToString())
	}
	return result
}

func NewEnv(stringEnv []string) *Env {
	env := &Env{}
	if stringEnv != nil {
		for _, rawEnvVar := range stringEnv {
			env.SetEnv(FromString(rawEnvVar))
		}
	}
	return env
}
