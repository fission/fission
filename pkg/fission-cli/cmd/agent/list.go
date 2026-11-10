// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"fmt"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	"github.com/fission/fission/pkg/fission-cli/cmd"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
	"github.com/fission/fission/pkg/fission-cli/util"
)

type ListSubCommand struct {
	cmd.CommandActioner
}

// List lists the functions declared as agents (FunctionSpec.Agent != nil). It
// reads the Function CRDs directly, like `fission fn tools` does for MCP
// tools; there is no agentruntime round-trip since agent declaration is
// static on the CRD.
func List(input cli.Input) error {
	return (&ListSubCommand{}).do(input)
}

func (opts *ListSubCommand) do(input cli.Input) error {
	namespace, err := opts.ResolveNamespace(input)
	if err != nil {
		return fmt.Errorf("error listing agent functions: %w", err)
	}

	fns, err := opts.Client().FissionClientSet.CoreV1().Functions(namespace).List(input.Context(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("error listing functions: %w", err)
	}

	format, err := util.ParseOutputFormat(input.String(flagkey.Output))
	if err != nil {
		return err
	}

	agents := make([]fv1.Function, 0, len(fns.Items))
	for _, f := range fns.Items {
		if f.Spec.Agent != nil {
			agents = append(agents, f)
		}
	}

	headers := []string{"NAME", "NAMESPACE", "SESSION-SOURCE", "IDLE-AFTER", "ARCHIVE-AFTER", "MAX-SESSIONS"}
	row := func(f fv1.Function) []string {
		return []string{
			f.Name,
			f.Namespace,
			sessionSource(f.Spec.Agent),
			durationOrDefault(f.Spec.Agent.IdleAfter),
			durationOrDefault(f.Spec.Agent.ArchiveAfter),
			maxSessions(f.Spec.Agent.MaxSessions),
		}
	}
	wideExtra := []string{"AGE"}
	wideRow := func(f fv1.Function) []string { return []string{util.AgeOf(f.CreationTimestamp)} }

	return util.PrintObjects(format, agents, headers, row, wideExtra, wideRow)
}

// sessionSource renders where the session id comes from, e.g. "header/X-Fission-Session".
// A nil Session (the default) means the X-Fission-Session request header.
func sessionSource(a *fv1.AgentConfig) string {
	if a.Session == nil {
		return string(fv1.SessionSourceHeader) + "/" + fv1.DefaultAgentSessionHeader
	}
	return string(a.Session.Source) + "/" + a.Session.Name
}

// durationOrDefault renders a *metav1.Duration, or "default" when unset (the
// platform default applies).
func durationOrDefault(d *metav1.Duration) string {
	if d == nil {
		return "default"
	}
	return d.Duration.String()
}

// maxSessions renders the live-session cap, or "unlimited" when zero.
func maxSessions(n int64) string {
	if n == 0 {
		return "unlimited"
	}
	return strconv.FormatInt(n, 10)
}
