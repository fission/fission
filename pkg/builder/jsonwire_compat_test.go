// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package builder

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compat gate for the json/v2 migration — cross-version RPC wire (rolling-upgrade
// skew; builder images and pool pods outlive releases); if this fails after a
// migration commit, add compat options at the marshal site, do not update the
// fixture.
//
// PackageBuildRequest/PackageBuildResponse are the buildermgr<->builder wire
// types (pkg/builder/client, and the Handler/reply methods in builder.go).
// The builder binary is baked into environment builder images that are NOT
// rebuilt per Fission release, so a buildermgr on a new release can be talking
// to a builder pod running an old image (and vice versa during a rolling
// upgrade) for a long skew window — any drift in these bytes breaks that pair
// silently. Both types happen to have no `omitempty` tags at all, so these
// fixtures pin plain field-value fidelity rather than the struct-omitempty
// defaulting risk (see pkg/executor/client and pkg/storagesvc for that case).

func TestPackageBuildRequestJSONWireCompat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		req    PackageBuildRequest
		golden string
	}{
		{
			name: "full",
			req: PackageBuildRequest{
				SrcPkgFilename: "pkg-abc123",
				BuildCommand:   "npm run build",
			},
			golden: `{"srcPkgFilename":"pkg-abc123","command":"npm run build"}`,
		},
		{
			name:   "zero_heavy",
			req:    PackageBuildRequest{},
			golden: `{"srcPkgFilename":"","command":""}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			body, err := json.Marshal(tc.req)
			require.NoError(t, err)
			require.Equal(t, tc.golden, string(body),
				"compat gate for the json/v2 migration — cross-version RPC wire "+
					"(rolling-upgrade skew; builder images and pool pods outlive "+
					"releases); if this fails after a migration commit, add compat "+
					"options at the marshal site, do not update the fixture")

			var decoded PackageBuildRequest
			require.NoError(t, json.Unmarshal([]byte(tc.golden), &decoded))
			assert.Equal(t, tc.req, decoded)
		})
	}
}

func TestPackageBuildResponseJSONWireCompat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		resp   PackageBuildResponse
		golden string
	}{
		{
			name: "full",
			resp: PackageBuildResponse{
				ArtifactFilename: "deploy-abc123-xyz",
				BuildLogs:        "build succeeded\ndone in 4.2s\n",
			},
			golden: `{"artifactFilename":"deploy-abc123-xyz","buildLogs":"build succeeded\ndone in 4.2s\n"}`,
		},
		{
			name:   "zero_heavy",
			resp:   PackageBuildResponse{},
			golden: `{"artifactFilename":"","buildLogs":""}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			body, err := json.Marshal(tc.resp)
			require.NoError(t, err)
			require.Equal(t, tc.golden, string(body),
				"compat gate for the json/v2 migration — cross-version RPC wire "+
					"(rolling-upgrade skew; builder images and pool pods outlive "+
					"releases); if this fails after a migration commit, add compat "+
					"options at the marshal site, do not update the fixture")

			var decoded PackageBuildResponse
			require.NoError(t, json.Unmarshal([]byte(tc.golden), &decoded))
			assert.Equal(t, tc.resp, decoded)
		})
	}
}
