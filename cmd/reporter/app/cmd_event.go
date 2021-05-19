/*
Copyright 2021 The Fission Authors.

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
package app

import (
	"github.com/fission/fission/pkg/tracker"
	"github.com/spf13/cobra"
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

	return tracker.Tracker.SendEvent(event)
}

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
	eventCmd.MarkPersistentFlagRequired("category")
	eventCmd.MarkPersistentFlagRequired("action")
	return eventCmd
}
