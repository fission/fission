// SPDX-FileCopyrightText: The Kubernetes Authors
//
// SPDX-License-Identifier: Apache-2.0

// Original file location: https://github.com/kubernetes/kubectl/tree/master/pkg/util/templates

package helptemplate

import (
	"strings"
	"unicode"
)

const (
	// SectionVars is the help template section that declares variables to be used in the template.
	SectionVars = `{{$isRootCmd := isRootCmd .}}` +
		`{{$rootCmd := rootCmd .}}` +
		`{{$visibleFlags := visibleFlags (flagsNotIntersected .LocalFlags .PersistentFlags)}}` +
		`{{$explicitlyExposedFlags := exposed .}}` +
		`{{$usageLine := usageLine .}}`

	// SectionAliases is the help template section that displays command aliases.
	SectionAliases = `{{if gt .Aliases 0}}Aliases:
  {{.NameAndAliases}}

{{end}}`

	// SectionExamples is the help template section that displays command examples.
	SectionExamples = `{{if .HasExample}}Examples:
  {{trimRight .Example}}

{{end}}`

	// SectionSubcommands is the help template section that displays the command's subcommands.
	SectionSubcommands = `{{if .HasAvailableSubCommands}}{{cmdGroupsString .}}

{{end}}`

	// SectionFlags is the help template section that displays the command's flags.
	SectionFlags = `{{ if $visibleFlags.HasFlags}}Options:
{{trimRight (flagsUsages $visibleFlags)}}

{{end}}`

	// SectionGlobalFlags is the help template section that displays the command's global flags.
	SectionGlobalFlags = `{{ if and (not $isRootCmd) (not .HasSubCommands) }}{{ if $explicitlyExposedFlags.HasFlags}}Global Options:
{{trimRight (flagsUsages $explicitlyExposedFlags)}}{{end}}

{{end}}`

	// SectionUsage is the help template section that displays the command's usage.
	SectionUsage = `{{if and .Runnable (ne .UseLine "") (ne .UseLine $rootCmd)}}Usage:
  {{$usageLine}}
{{end}}`

	// SectionTipsHelp is the help template section that displays the '--help' hint.
	SectionTipsHelp = `{{if .HasSubCommands}}Use "{{$rootCmd}} <command> --help" for more information about a given command.
{{end}}`
)

// MainHelpTemplate if the template for 'help' used by most commands.
func MainHelpTemplate() string {
	return `{{with or .Long .Short }}{{. | trimRight}}
{{end}}{{if or .Runnable .HasSubCommands}}{{.UsageString}}{{end}}`
}

// MainUsageTemplate if the template for 'usage' used by most commands.
func MainUsageTemplate() string {
	sections := []string{
		"\n",
		SectionVars,
		SectionAliases,
		SectionExamples,
		SectionSubcommands,
		SectionFlags,
		SectionGlobalFlags,
		SectionUsage,
		SectionTipsHelp,
	}
	return strings.TrimRightFunc(strings.Join(sections, ""), unicode.IsSpace)
}
