// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"log"

	"github.com/spf13/cobra"

	"github.com/fission/fission/pkg/tracker"
)

func eventCommandHandler(cmd *cobra.Command, args []string) error {
	var err error
	flags := cmd.PersistentFlags()

	event := tracker.Event{}

	event.Category, err = flags.GetString("category")
	if err != nil {
		return err
	}

	event.Action, err = flags.GetString("action")
	if err != nil {
		return err
	}

	event.Label, err = flags.GetString("label")
	if err != nil {
		return err
	}

	event.Value, err = flags.GetString("value")
	if err != nil {
		return err
	}

	t, err := tracker.NewTracker()
	if err != nil {
		return err
	}
	return t.SendEvent(cmd.Context(), event)
}

// EventCommand reports an event to analytics
func EventCommand() *cobra.Command {
	eventCmd := &cobra.Command{
		Use:   "event",
		Short: "Report event to analytics",
		RunE:  eventCommandHandler,
		Args:  cobra.NoArgs,
	}
	persistentFlags := eventCmd.PersistentFlags()
	persistentFlags.StringP("category", "c", "", "event category")
	persistentFlags.StringP("action", "a", "", "event action")
	persistentFlags.StringP("label", "l", "", "event label")
	persistentFlags.StringP("value", "v", "", "event value")
	err := eventCmd.MarkPersistentFlagRequired("category")
	if err != nil {
		log.Fatal(err)
	}
	err = eventCmd.MarkPersistentFlagRequired("action")
	if err != nil {
		log.Fatal(err)
	}
	return eventCmd
}
