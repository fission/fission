// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

func TestGetAgentConfig(t *testing.T) {
	t.Parallel()

	t.Run("FnAgent unset -> nil returned", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		ac := getAgentConfig(in, nil)
		assert.Nil(t, ac)
	})

	t.Run("FnAgent set, nothing else -> all platform defaults", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.SetBool(flagkey.FnAgent, true)
		ac := getAgentConfig(in, nil)
		assert.Equal(t, &fv1.AgentConfig{}, ac)
	})

	t.Run("session source+name set -> Session populated", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.SetBool(flagkey.FnAgent, true)
		in.SetString(flagkey.FnAgentSessionSource, string(fv1.SessionSourceQueryParam))
		in.SetString(flagkey.FnAgentSessionName, "sid")
		ac := getAgentConfig(in, nil)
		assert.Equal(t, &fv1.AgentConfig{
			Session: &fv1.AgentSessionConfig{Source: fv1.SessionSourceQueryParam, Name: "sid"},
		}, ac)
	})

	t.Run("idle/archive/max set -> fields populated", func(t *testing.T) {
		t.Parallel()
		in := dummy.TestFlagSet()
		in.SetBool(flagkey.FnAgent, true)
		in.SetDuration(flagkey.FnAgentIdleAfter, 10*time.Minute)
		in.SetDuration(flagkey.FnAgentArchiveAfter, 48*time.Hour)
		in.SetInt(flagkey.FnAgentMaxSessions, 25)
		ac := getAgentConfig(in, nil)
		assert.Equal(t, &fv1.AgentConfig{
			IdleAfter:    &metav1.Duration{Duration: 10 * time.Minute},
			ArchiveAfter: &metav1.Duration{Duration: 48 * time.Hour},
			MaxSessions:  25,
		}, ac)
	})

	t.Run("existing config + only FnAgentMaxSessions set -> other fields preserved", func(t *testing.T) {
		t.Parallel()
		existing := &fv1.AgentConfig{
			Session:      &fv1.AgentSessionConfig{Source: fv1.SessionSourceHeader, Name: "X-Custom-Session"},
			IdleAfter:    &metav1.Duration{Duration: 5 * time.Minute},
			ArchiveAfter: &metav1.Duration{Duration: 24 * time.Hour},
			MaxSessions:  10,
		}
		in := dummy.TestFlagSet()
		in.SetBool(flagkey.FnAgent, true)
		in.SetInt(flagkey.FnAgentMaxSessions, 50)
		ac := getAgentConfig(in, existing)
		assert.Equal(t, &fv1.AgentConfig{
			Session:      &fv1.AgentSessionConfig{Source: fv1.SessionSourceHeader, Name: "X-Custom-Session"},
			IdleAfter:    &metav1.Duration{Duration: 5 * time.Minute},
			ArchiveAfter: &metav1.Duration{Duration: 24 * time.Hour},
			MaxSessions:  50,
		}, ac)
		assert.Equal(t, int64(10), existing.MaxSessions, "the original is not mutated")
	})
}
