// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"fmt"
	"time"

	"github.com/fission/fission/test/benchmark/pkg/harness"
)

// measureColdStart creates one function from fnOpts (Name/Env are set here,
// any value the caller put there is overwritten) plus a route, measures the
// first successful request via measureFirstSuccess, and tears the pair down
// in its own detached sub-scope regardless of outcome. label distinguishes
// callers' sub-scope names (e.g. "i" for a plain cold-start loop,
// "baseline-i"/"treatment-i" for a two-phase scenario), so concurrent phases
// or scenarios sharing a Scope don't collide on resource names.
func measureColdStart(ctx context.Context, sc *harness.Scope, envName, label string, i int, fnOpts harness.FunctionOptions) (time.Duration, bool) {
	env := sc.Env()
	iter := sc.SubScope(fmt.Sprintf("%s%d", label, i))
	defer iter.CleanupDetached(ctx, time.Minute)

	fnOpts.Name = iter.Name("fn")
	fnOpts.Env = envName
	route := "/" + fnOpts.Name
	if err := iter.CreateCodeFunction(ctx, fnOpts); err != nil {
		return 0, false
	}
	if err := iter.CreateRoute(ctx, harness.RouteOptions{Function: fnOpts.Name, URL: route}); err != nil {
		return 0, false
	}
	return measureFirstSuccess(ctx, env.RouterURL()+route, 3*time.Minute)
}
