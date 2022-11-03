/*
Copyright 2018 The Fission Authors.

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

package resources

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type FissionVersion struct {
	client cmd.Client
}

func NewFissionVersion(client cmd.Client) Resource {
	return FissionVersion{client: client}
}

func (res FissionVersion) Dump(ctx context.Context, dumpDir string) {
	ver := util.GetVersion(ctx, res.client)
	file := filepath.Clean(fmt.Sprintf("%v/%v", dumpDir, "fission-version.txt"))
	writeToFile(file, ver)
}
