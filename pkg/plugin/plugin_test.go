/*
Copyright 2016 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"testing"
	"time"

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
