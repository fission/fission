// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type FissionVersion struct {
	client cmd.Client
	input  cli.Input
}

func NewFissionVersion(client cmd.Client, input cli.Input) Resource {
	return FissionVersion{client: client, input: input}
}

func (res FissionVersion) Dump(ctx context.Context, dumpDir string) {
	ver := util.GetVersion(ctx, res.input, res.client)
	file := filepath.Clean(fmt.Sprintf("%v/%v", dumpDir, "fission-version.txt"))
	writeToFile(file, ver)
}
