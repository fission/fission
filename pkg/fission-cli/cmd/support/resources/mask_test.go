// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMaskCredentialValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		in     map[string]string
		want   map[string]string
		masked bool
	}{
		{
			name:   "credential-named keys are masked, keys preserved",
			in:     map[string]string{"password": "hunter2", "topic": "orders"},
			want:   map[string]string{"password": "-", "topic": "orders"},
			masked: true,
		},
		{
			name:   "a url carrying a password is masked whatever the key",
			in:     map[string]string{"host": "amqp://user:hunter2@rabbit:5672/"},
			want:   map[string]string{"host": "-"},
			masked: true,
		},
		{
			name: "a url with no password is configuration, not a secret",
			in:   map[string]string{"host": "amqp://rabbit:5672/", "address": "redis:6379"},
			want: map[string]string{"host": "amqp://rabbit:5672/", "address": "redis:6379"},
		},
		{
			name: "sasl names a mechanism, not a secret",
			in:   map[string]string{"sasl": "scram_sha512", "saslType": "plaintext"},
			want: map[string]string{"sasl": "scram_sha512", "saslType": "plaintext"},
		},
		{
			name:   "saslPassword is a secret",
			in:     map[string]string{"saslPassword": "hunter2"},
			want:   map[string]string{"saslPassword": "-"},
			masked: true,
		},
		{
			name:   "matching is case-insensitive and substring-based",
			in:     map[string]string{"AwsAccessKeyID": "AKIA", "bearerToken": "t"},
			want:   map[string]string{"AwsAccessKeyID": "-", "bearerToken": "-"},
			masked: true,
		},
		{
			name: "an empty value is left alone, so absent stays distinguishable",
			in:   map[string]string{"password": ""},
			want: map[string]string{"password": ""},
		},
		{name: "nil in, nil out"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, masked := maskCredentialValues(tc.in)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.masked, masked)
		})
	}
}

func TestMaskCredentialValuesDoesNotMutateItsInput(t *testing.T) {
	t.Parallel()

	in := map[string]string{"password": "hunter2"}
	got, _ := maskCredentialValues(in)

	assert.Equal(t, "-", got["password"])
	assert.Equal(t, "hunter2", in["password"], "the caller's map is shared with the List result")
}
