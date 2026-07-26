// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package functionalias

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/conditions"
	"github.com/fission/fission/pkg/fission-cli/util"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
)

// waitForResolved polls get until the FunctionAlias's Resolved condition is
// True and (when wantVersion is non-empty) Status.ResolvedVersion equals
// wantVersion, or ctx is done. wantVersion=="" only waits for Resolved=True —
// the shape `alias update --wait` needs for a PackageDigest-pinned alias,
// where resolution happens asynchronously against a target the caller cannot
// name in advance (see FunctionAliasSpec's XOR of Version/PackageDigest).
// `fn rollback --wait` always passes the explicit target version it just set.
// interval is the poll period; mirrors function.waitForPackageBuild. The poll
// scaffold itself is util.PollUntil, shared with util.WaitForCondition and
// function.waitForPackageBuild.
func waitForResolved(ctx context.Context, get func(context.Context) (*fv1.FunctionAlias, error), wantVersion string, interval time.Duration) error {
	check := func(ctx context.Context) (bool, error) {
		alias, err := get(ctx)
		switch {
		case err == nil:
			if conditions.IsTrue(alias.Status.Conditions, fv1.FunctionAliasConditionResolved) &&
				(wantVersion == "" || alias.Status.ResolvedVersion == wantVersion) {
				return true, nil
			}
		case !util.IsNotFound(err):
			return false, err
		}
		return false, nil
	}

	if err := util.PollUntil(ctx, interval, check); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s waiting for alias to resolve%s: %w", util.PollDeadlineVerb(ctx), resolveWaitSuffix(wantVersion), ctx.Err())
		}
		return err
	}
	return nil
}

func resolveWaitSuffix(wantVersion string) string {
	if wantVersion == "" {
		return ""
	}
	return fmt.Sprintf(" to %q", wantVersion)
}

// WaitForResolved is waitForResolved's public entry point, used by both
// `alias update --wait` and `fn rollback --wait`: it polls the named
// FunctionAlias at 1s intervals until Resolved=True (and, when wantVersion is
// set, Status.ResolvedVersion==wantVersion) or timeout elapses (falling back
// to util.DefaultWaitTimeout when timeout<=0, mirroring util.RunWait).
func WaitForResolved(ctx context.Context, cl versioned.Interface, namespace, name, wantVersion string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = util.DefaultWaitTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	get := func(ctx context.Context) (*fv1.FunctionAlias, error) {
		return cl.CoreV1().FunctionAliases(namespace).Get(ctx, name, metav1.GetOptions{})
	}
	return waitForResolved(ctx, get, wantVersion, time.Second)
}
