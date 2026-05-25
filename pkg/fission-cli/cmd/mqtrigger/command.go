// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqtrigger

import (
	"github.com/spf13/cobra"

	wrapper "github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/cobra"
	"github.com/fission/fission/pkg/fission-cli/flag"
)

func Commands() *cobra.Command {
	createCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "create",
		Short: "Create a message queue trigger",
	}, Create, flag.FlagSet{
		Required: []flag.Flag{flag.MqtFnName, flag.MqtTopic},
		Optional: []flag.Flag{flag.MqtName, flag.MqtMQType, flag.MqtRespTopic,
			flag.MqtErrorTopic, flag.MqtMaxRetries, flag.MqtMsgContentType,
			flag.NamespaceFunction, flag.SpecSave, flag.SpecDry, flag.MqtPollingInterval,
			flag.MqtCooldownPeriod, flag.MqtMinReplicaCount, flag.MqtMaxReplicaCount, flag.MqtSecret,
			flag.MqtMetadata, flag.MqtKind},
	})

	updateCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "update",
		Aliases: []string{},
		Short:   "Update a message queue trigger",
	}, Update, flag.FlagSet{
		Required: []flag.Flag{flag.MqtName},
		Optional: []flag.Flag{flag.MqtFnName, flag.MqtTopic, flag.MqtRespTopic, flag.MqtErrorTopic,
			flag.MqtMaxRetries, flag.MqtMsgContentType, flag.NamespaceTrigger, flag.MqtPollingInterval,
			flag.MqtCooldownPeriod, flag.MqtMinReplicaCount, flag.MqtMaxReplicaCount, flag.MqtMetadata,
			flag.MqtSecret, flag.MqtKind},
	})

	deleteCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "delete",
		Aliases: []string{},
		Short:   "Delete a message queue trigger",
	}, Delete, flag.FlagSet{
		Required: []flag.Flag{flag.MqtName},
		Optional: []flag.Flag{flag.NamespaceTrigger, flag.IgnoreNotFound},
	})

	listCmd := wrapper.SubCommand(&cobra.Command{
		Use:     "list",
		Aliases: []string{},
		Short:   "List message queue triggers",
		Long:    "List all message queue triggers in a namespace if specified, else, list message queue triggers across all namespaces",
	}, List, flag.FlagSet{
		Optional: []flag.Flag{flag.NamespaceTrigger, flag.AllNamespaces, flag.Output},
	})

	command := &cobra.Command{
		Use:     "mqtrigger",
		Aliases: []string{"mqt"},
		Short:   "Create, update and manage message queue triggers",
	}

	waitCmd := wrapper.SubCommand(&cobra.Command{
		Use:   "wait",
		Short: "Wait for a message queue trigger to reach a status condition",
	}, Wait, flag.FlagSet{
		Required: []flag.Flag{flag.MqtName, flag.WaitFor},
		Optional: []flag.Flag{flag.NamespaceTrigger, flag.WaitTimeout},
	})

	command.AddCommand(createCmd, updateCmd, deleteCmd, listCmd, waitCmd)

	return command
}
