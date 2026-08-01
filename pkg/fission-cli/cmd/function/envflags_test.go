// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitEnvSourceRef(t *testing.T) {
	t.Parallel()

	cases := []struct {
		ref                       string
		wantObj, wantKey, wantEnv string
		wantErr                   bool
	}{
		{ref: "creds", wantObj: "creds"},
		{ref: "creds/token", wantObj: "creds", wantKey: "token", wantEnv: "token"},
		{ref: "creds/token:API_TOKEN", wantObj: "creds", wantKey: "token", wantEnv: "API_TOKEN"},
		{ref: "creds/token:", wantErr: true},
		{ref: "creds:API_TOKEN", wantErr: true},
		{ref: "/token", wantErr: true},
		{ref: "creds/", wantErr: true},
		{ref: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			t.Parallel()
			obj, key, env, err := splitEnvSourceRef("env-from-secret", tc.ref)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantObj, obj)
			assert.Equal(t, tc.wantKey, key)
			assert.Equal(t, tc.wantEnv, env)
		})
	}
}
