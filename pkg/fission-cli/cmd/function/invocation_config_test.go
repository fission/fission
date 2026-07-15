// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

// fakeInvInput overrides the accessors getInvocationConfig reads.
type fakeInvInput struct {
	cli.Input
	set map[string]bool
	i   map[string]int
	d   map[string]time.Duration
	s   map[string]string
}

func (f fakeInvInput) IsSet(k string) bool             { return f.set[k] }
func (f fakeInvInput) Int(k string) int                { return f.i[k] }
func (f fakeInvInput) Duration(k string) time.Duration { return f.d[k] }
func (f fakeInvInput) String(k string) string          { return f.s[k] }

func TestGetInvocationConfig(t *testing.T) {
	t.Parallel()

	t.Run("nothing set, no existing → nil", func(t *testing.T) {
		assert.Nil(t, getInvocationConfig(fakeInvInput{}, nil))
	})

	t.Run("nothing set keeps existing untouched", func(t *testing.T) {
		existing := &fv1.InvocationConfig{MaxAge: &metav1.Duration{Duration: time.Hour}}
		assert.Same(t, existing, getInvocationConfig(fakeInvInput{}, existing))
	})

	t.Run("all fields from flags", func(t *testing.T) {
		in := fakeInvInput{
			set: map[string]bool{flagkey.FnAsyncMaxAttempts: true, flagkey.FnAsyncMaxAge: true, flagkey.FnAsyncOnSuccess: true, flagkey.FnAsyncOnFailure: true},
			i:   map[string]int{flagkey.FnAsyncMaxAttempts: 3},
			d:   map[string]time.Duration{flagkey.FnAsyncMaxAge: 2 * time.Hour},
			s:   map[string]string{flagkey.FnAsyncOnSuccess: "notify", flagkey.FnAsyncOnFailure: "alert"},
		}
		ic := getInvocationConfig(in, nil)
		require.NotNil(t, ic)
		require.NotNil(t, ic.Retry.MaxAttempts)
		assert.Equal(t, 3, *ic.Retry.MaxAttempts)
		require.NotNil(t, ic.MaxAge)
		assert.Equal(t, 2*time.Hour, ic.MaxAge.Duration)
		require.NotNil(t, ic.OnSuccess.Function)
		assert.Equal(t, "notify", ic.OnSuccess.Function.Name)
		assert.EqualValues(t, fv1.FunctionReferenceTypeFunctionName, ic.OnSuccess.Function.Type)
		assert.Equal(t, "alert", ic.OnFailure.Function.Name)
	})

	t.Run("merge onto existing keeps unset fields", func(t *testing.T) {
		existing := &fv1.InvocationConfig{
			OnSuccess: &fv1.DestinationRef{Function: &fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "keep"}},
			MaxAge:    &metav1.Duration{Duration: time.Hour},
		}
		in := fakeInvInput{
			set: map[string]bool{flagkey.FnAsyncMaxAttempts: true},
			i:   map[string]int{flagkey.FnAsyncMaxAttempts: 5},
		}
		ic := getInvocationConfig(in, existing)
		require.NotNil(t, ic.Retry.MaxAttempts)
		assert.Equal(t, 5, *ic.Retry.MaxAttempts)
		require.NotNil(t, ic.OnSuccess, "existing OnSuccess preserved")
		assert.Equal(t, "keep", ic.OnSuccess.Function.Name)
		assert.Equal(t, time.Hour, ic.MaxAge.Duration, "existing MaxAge preserved")
		assert.Nil(t, existing.Retry.MaxAttempts, "the original is not mutated")
	})

	t.Run("empty destination clears it", func(t *testing.T) {
		existing := &fv1.InvocationConfig{
			OnFailure: &fv1.DestinationRef{Function: &fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: "old"}},
		}
		in := fakeInvInput{
			set: map[string]bool{flagkey.FnAsyncOnFailure: true},
			s:   map[string]string{flagkey.FnAsyncOnFailure: ""},
		}
		ic := getInvocationConfig(in, existing)
		assert.Nil(t, ic.OnFailure, "empty --async-on-failure clears the destination")
	})
}
