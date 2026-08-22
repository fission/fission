// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package plugin

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFind(t *testing.T) {
	os.Clearenv()
	testDir := path.Join(os.TempDir(), fmt.Sprintf("fission-test-plugins-%v", time.Now().UnixNano()))
	err := os.MkdirAll(testDir, os.ModePerm)
	require.NoError(t, err)
	defer os.RemoveAll(testDir)
	testBinary := path.Join(testDir, "foo")
	md := &Metadata{
		Name:    "foo",
		Version: "1.0.1",
		Usage:   "Usage help",
		Aliases: []string{"bar"},
	}
	jsonMd, err := json.Marshal(md)
	require.NoError(t, err)
	err = os.WriteFile(testBinary, fmt.Appendf(nil, "#!/bin/sh\necho '%v'", string(jsonMd)), os.ModePerm)
	require.NoError(t, err)

	err = os.Setenv("PATH", testDir)
	require.NoError(t, err)
	Prefix = ""

	ctx := t.Context()

	found, err := Find(ctx, md.Name)
	os.RemoveAll(testDir)
	require.NoError(t, err)
	require.NotEmpty(t, found)
	require.Equal(t, md.Name, found.Name)
	require.Equal(t, path.Join(testDir, md.Name), found.Path)
	require.Equal(t, md.Aliases, found.Aliases)
	require.Equal(t, md.Usage, found.Usage)
	require.Equal(t, md.Version, found.Version)
}

func TestExec(t *testing.T) {
	os.Clearenv()
	testDir := path.Join(os.TempDir(), fmt.Sprintf("fission-test-plugins-%v", time.Now().UnixNano()))
	err := os.MkdirAll(testDir, os.ModePerm)
	require.NoError(t, err)
	defer os.RemoveAll(testDir)
	testBinary := path.Join(testDir, "foo")
	md := &Metadata{
		Name:    "foo",
		Version: "1.0.1",
		Usage:   "Usage help",
		Aliases: []string{"bar"},
		Path:    path.Join(testBinary),
	}
	jsonMd, err := json.Marshal(md)
	require.NoError(t, err)
	err = os.WriteFile(testBinary, fmt.Appendf(nil, "#!/bin/sh\necho '%v'", string(jsonMd)), os.ModePerm)
	require.NoError(t, err)
	err = os.Setenv("PATH", testDir)
	require.NoError(t, err)
	Prefix = ""
	err = Exec(md, nil)
	os.RemoveAll(testDir)
	require.NoError(t, err)
}

// TestMetadataDecodeLeniency pins the v1-equivalent leniency contract for
// third-party plugin metadata: case-insensitive field matching, duplicate
// keys tolerated last-wins, invalid UTF-8 substituted rather than rejected.
// If metadataDecodeOpts loses an option, this fails instead of plugins
// silently losing fields.
func TestMetadataDecodeLeniency(t *testing.T) {
	t.Parallel()

	t.Run("capitalized field names still match", func(t *testing.T) {
		t.Parallel()
		md := &Metadata{}
		require.NoError(t, json.Unmarshal([]byte(`{"Name":"hank","Version":"1.0"}`), md, metadataDecodeOpts))
		assert.Equal(t, "hank", md.Name)
		assert.Equal(t, "1.0", md.Version)
	})
	t.Run("duplicate keys last-wins", func(t *testing.T) {
		t.Parallel()
		md := &Metadata{}
		require.NoError(t, json.Unmarshal([]byte(`{"name":"a","name":"b"}`), md, metadataDecodeOpts))
		assert.Equal(t, "b", md.Name)
	})
	t.Run("invalid UTF-8 substituted not rejected", func(t *testing.T) {
		t.Parallel()
		md := &Metadata{}
		require.NoError(t, json.Unmarshal([]byte("{\"name\":\"x\xff\"}"), md, metadataDecodeOpts))
		assert.Equal(t, "x�", md.Name)
	})
}
