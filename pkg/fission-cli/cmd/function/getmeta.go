// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/versioning"
)

type GetMetaSubCommand struct {
	cmd.CommandActioner
}

func GetMeta(input cli.Input) error {
	return (&GetMetaSubCommand{}).do(input)
}

func (opts *GetMetaSubCommand) do(input cli.Input) error {
	_, namespace, err := opts.GetResourceNamespace(input)
	if err != nil {
		return fmt.Errorf("error in getting meta function : %w", err)
	}

	fn, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).Get(input.Context(), input.String(flagkey.FnName), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting function: %w", err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}
	if handled, err := util.PrintStructured(format, fn); err != nil || handled {
		return err
	}

	fmt.Printf("Name: %v\n", fn.Name)
	fmt.Printf("Environment: %v\n", fn.Spec.Environment.Name)
	if len(fn.Labels) != 0 {
		fmt.Println("Labels:")
		for k, v := range fn.Labels {
			fmt.Printf("  %s=%s\n", k, v)
		}
	}
	if len(fn.Annotations) != 0 {
		fmt.Println("Annotations:")
		for k, v := range fn.Annotations {
			fmt.Printf("  %s=%s\n", k, v)
		}
	}
	if line := versioningSummary(input.Context(), opts.Client(), namespace, fn); line != "" {
		fmt.Println(line)
	}
	util.PrintConditions(fn.Status.Conditions)

	return nil
}

// versioningSummary renders the one-liner RFC-0025 addition, e.g.
// "versioning: auto (3 versions)" -- empty (nothing printed) for a function
// that never opted into versioning. The version count is a best-effort
// versioning.ListVersionsForFunction call (same selector as describe.go's
// versionsFor): an unreadable list degrades the count to 0 rather than
// failing getmeta.
func versioningSummary(ctx context.Context, client cmd.Client, namespace string, fn *fv1.Function) string {
	if fn.Spec.Versioning == nil {
		return ""
	}
	mode := fn.Spec.Versioning.Mode
	if mode == "" {
		mode = fv1.VersioningModeAuto
	}
	items, err := versioning.ListVersionsForFunction(ctx, client.FissionClientSet, namespace, fn.Name)
	count := 0
	if err == nil {
		count = len(items)
	}
	return fmt.Sprintf("versioning: %s (%d versions)", mode, count)
}
