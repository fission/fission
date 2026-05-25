// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package helptemplate

import (
	"github.com/spf13/cobra"
)

func CreateCmdGroup(msg string, cmds ...*cobra.Command) CommandGroup {
	return CommandGroup{
		Message:  msg,
		Commands: cmds,
	}
}
