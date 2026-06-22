// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package timetrigger

import (
	"fmt"
	"time"

	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/utils/uuid"

	"errors"

	"github.com/robfig/cron/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd/spec"
	"github.com/fission/fission/pkg/fission-cli/console"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type CreateSubCommand struct {
	cmd.CommandActioner
	trigger *fv1.TimeTrigger
}

func Create(input cli.Input) error {
	return (&CreateSubCommand{}).do(input)
}

func (opts *CreateSubCommand) do(input cli.Input) error {
	err := opts.complete(input)
	if err != nil {
		return err
	}
	return opts.run(input)
}

func (opts *CreateSubCommand) complete(input cli.Input) (err error) {
	name := input.String(flagkey.TtName)
	if len(name) == 0 {
		name = uuid.NewString()
	}

	fnName := input.String(flagkey.TtFnName)
	if len(fnName) == 0 {
		return errors.New("need a function name to create a trigger, use --function")
	}

	userProvidedNS, fnNamespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in creating time trigger : %w", err)
	}

	cronSpec := input.String(flagkey.TtCron)
	if len(cronSpec) == 0 {
		return errors.New("need a cron spec like '30 * * * *', '@every 1h30m', or '@hourly'; use --cron")
	}

	if input.Bool(flagkey.SpecSave) {
		specDir := util.GetSpecDir(input)
		specIgnore := util.GetSpecIgnore(input)
		fr, err := spec.ReadSpecs(specDir, specIgnore, false)
		if err != nil {
			return fmt.Errorf("error reading spec in '%v': %w", specDir, err)
		}

		exists, err := fr.ExistsInSpecs(fv1.Function{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fnName,
				Namespace: userProvidedNS,
			},
		})
		if err != nil {
			return err
		}
		if !exists {
			console.Warn(fmt.Sprintf("TimeTrigger '%s' references unknown Function '%s', please create it before applying spec",
				name, fnName))
		}
	}

	m := metav1.ObjectMeta{
		Name:      name,
		Namespace: fnNamespace,
	}

	if input.Bool(flagkey.SpecSave) || input.Bool(flagkey.SpecDry) {
		m = metav1.ObjectMeta{
			Name:      name,
			Namespace: userProvidedNS,
		}
	}

	opts.trigger = &fv1.TimeTrigger{
		ObjectMeta: m,
		Spec: fv1.TimeTriggerSpec{
			Cron: cronSpec,
			FunctionReference: fv1.FunctionReference{
				Type: fv1.FunctionReferenceTypeFunctionName,
				Name: fnName,
			},
			Method:  input.String(flagkey.TtMethod),
			Subpath: input.String(flagkey.FnSubPath),
		},
	}

	return nil
}

func (opts *CreateSubCommand) run(input cli.Input) error {
	// if we're writing a spec, don't call the API; save/print and return.
	if handled, err := spec.SaveOrDry(input, *opts.trigger, fmt.Sprintf("timetrigger-%v.yaml", opts.trigger.Name)); handled {
		return err
	}

	_, err := opts.Client().FissionClientSet.CoreV1().TimeTriggers(opts.trigger.Namespace).Create(input.Context(), opts.trigger, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("error creating Time trigger: %w", err)
	}

	fmt.Printf("trigger '%v' created\n", opts.trigger.Name)

	t := util.GetServerInfo(input, opts.Client()).ServerTime.CurrentTime.UTC()

	err = getCronNextNActivationTime(opts.trigger.Spec.Cron, t, 1)
	if err != nil {
		return fmt.Errorf("error passing cron spec examination: %w", err)
	}

	return nil
}

func getCronNextNActivationTime(cronSpec string, serverTime time.Time, round int) error {
	cronSpecParser := cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	sched, err := cronSpecParser.Parse(cronSpec)
	if err != nil {
		return err
	}

	fmt.Printf("Current Server Time: \t%v\n", serverTime.Format(time.RFC3339))

	for i := range round {
		serverTime = sched.Next(serverTime)
		fmt.Printf("Next %v invocation: \t%v\n", i+1, serverTime.Format(time.RFC3339))
	}

	return nil
}
