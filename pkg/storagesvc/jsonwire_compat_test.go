// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compat gate for the json/v2 migration — cross-version RPC wire (rolling-upgrade
// skew; builder images and pool pods outlive releases); if this fails after a
// migration commit, add compat options at the marshal site, do not update the
// fixture.
//
// storagesvc.go's uploadHandler (~line 182) marshals *UploadResponse and
// listItems (~line 105) marshals a bare []string of archive ids as HTTP
// response bodies that pkg/storagesvc/client decodes. UploadResponse has no
// omitempty tags, so its shape is stable regardless of field values; the bare
// []string is the sharpest form of the json/v2 nil-slice risk this migration
// raises, because there is no struct/omitempty involved at all — v1's
// json.Marshal encodes a nil []string as the literal 4 bytes `null`, while a
// non-nil empty []string{} encodes as `[]`. Those are two different wire
// values today; a migration that normalizes them (e.g. always emitting `[]`)
// changes what pkg/storagesvc/client.List sees on the wire during a rolling
// upgrade against an old storagesvc, or vice versa.

func TestUploadResponseJSONWireCompat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		resp   UploadResponse
		golden string
	}{
		{
			name:   "full",
			resp:   UploadResponse{ID: "test-ns-a1b2c3d4"},
			golden: `{"id":"test-ns-a1b2c3d4"}`,
		},
		{
			name:   "zero_heavy",
			resp:   UploadResponse{},
			golden: `{"id":""}`,
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

			var decoded UploadResponse
			require.NoError(t, json.Unmarshal([]byte(tc.golden), &decoded))
			assert.Equal(t, tc.resp, decoded)
		})
	}
}

func TestArchiveListJSONWireCompat(t *testing.T) {
	t.Parallel()

	t.Run("populated", func(t *testing.T) {
		t.Parallel()

		ids := []string{"default-abc123", "test-ns-def456"}
		golden := `["default-abc123","test-ns-def456"]`

		body, err := json.Marshal(ids, listWireOpts)
		require.NoError(t, err)
		require.Equal(t, golden, string(body),
			"compat gate for the json/v2 migration — cross-version RPC wire "+
				"(rolling-upgrade skew; builder images and pool pods outlive "+
				"releases); if this fails after a migration commit, add compat "+
				"options at the marshal site, do not update the fixture")

		var decoded []string
		require.NoError(t, json.Unmarshal([]byte(golden), &decoded))
		assert.Equal(t, ids, decoded)
	})

	t.Run("nil_slice_marshals_null", func(t *testing.T) {
		t.Parallel()

		var ids []string
		golden := `null`

		body, err := json.Marshal(ids, listWireOpts)
		require.NoError(t, err)
		require.Equal(t, golden, string(body),
			"compat gate for the json/v2 migration — cross-version RPC wire "+
				"(rolling-upgrade skew; builder images and pool pods outlive "+
				"releases); if this fails after a migration commit, add compat "+
				"options at the marshal site, do not update the fixture")

		var decoded []string
		require.NoError(t, json.Unmarshal([]byte(golden), &decoded))
		assert.Nil(t, decoded)
	})

	t.Run("empty_non_nil_slice_marshals_empty_array", func(t *testing.T) {
		t.Parallel()

		ids := []string{}
		golden := `[]`

		body, err := json.Marshal(ids, listWireOpts)
		require.NoError(t, err)
		require.Equal(t, golden, string(body),
			"compat gate for the json/v2 migration — cross-version RPC wire "+
				"(rolling-upgrade skew; builder images and pool pods outlive "+
				"releases); if this fails after a migration commit, add compat "+
				"options at the marshal site, do not update the fixture")

		var decoded []string
		require.NoError(t, json.Unmarshal([]byte(golden), &decoded))
		assert.NotNil(t, decoded)
		assert.Empty(t, decoded)
	})
}
