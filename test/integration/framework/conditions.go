//go:build integration

package framework

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/conditions"
)

// GetFunctionConditions returns the Conditions slice on the named Function's
// Status, or nil if the controller hasn't populated it yet. Returns nil for
// resources whose controllers don't write conditions.
func (ns *TestNamespace) GetFunctionConditions(t *testing.T, ctx context.Context, name string) []metav1.Condition {
	t.Helper()
	fn, err := ns.f.fissionClient.CoreV1().Functions(ns.Name).Get(ctx, name, metav1.GetOptions{})
	require.NoErrorf(t, err, "GetFunctionConditions: get function %q", name)
	return fn.Status.Conditions
}

// GetPackageConditions returns the Conditions slice on the named Package's
// Status. Package status is still written via the main resource (no
// subresource), so this reflects whatever controller most recently wrote
// .status. See pkg/buildermgr/pkgwatcher.go.
func (ns *TestNamespace) GetPackageConditions(t *testing.T, ctx context.Context, name string) []metav1.Condition {
	t.Helper()
	pkg, err := ns.f.fissionClient.CoreV1().Packages(ns.Name).Get(ctx, name, metav1.GetOptions{})
	require.NoErrorf(t, err, "GetPackageConditions: get package %q", name)
	return pkg.Status.Conditions
}

// GetHTTPTriggerConditions returns the Conditions slice on the named
// HTTPTrigger's Status.
func (ns *TestNamespace) GetHTTPTriggerConditions(t *testing.T, ctx context.Context, name string) []metav1.Condition {
	t.Helper()
	r, err := ns.f.fissionClient.CoreV1().HTTPTriggers(ns.Name).Get(ctx, name, metav1.GetOptions{})
	require.NoErrorf(t, err, "GetHTTPTriggerConditions: get httptrigger %q", name)
	return r.Status.Conditions
}

// GetKubernetesWatchTriggerConditions returns the Conditions slice on the
// named KubernetesWatchTrigger's Status.
func (ns *TestNamespace) GetKubernetesWatchTriggerConditions(t *testing.T, ctx context.Context, name string) []metav1.Condition {
	t.Helper()
	r, err := ns.f.fissionClient.CoreV1().KubernetesWatchTriggers(ns.Name).Get(ctx, name, metav1.GetOptions{})
	require.NoErrorf(t, err, "GetKubernetesWatchTriggerConditions: get kuberneteswatchtrigger %q", name)
	return r.Status.Conditions
}

// GetTimeTriggerConditions returns the Conditions slice on the named
// TimeTrigger's Status.
func (ns *TestNamespace) GetTimeTriggerConditions(t *testing.T, ctx context.Context, name string) []metav1.Condition {
	t.Helper()
	r, err := ns.f.fissionClient.CoreV1().TimeTriggers(ns.Name).Get(ctx, name, metav1.GetOptions{})
	require.NoErrorf(t, err, "GetTimeTriggerConditions: get timetrigger %q", name)
	return r.Status.Conditions
}

// GetMessageQueueTriggerConditions returns the Conditions slice on the named
// MessageQueueTrigger's Status.
func (ns *TestNamespace) GetMessageQueueTriggerConditions(t *testing.T, ctx context.Context, name string) []metav1.Condition {
	t.Helper()
	r, err := ns.f.fissionClient.CoreV1().MessageQueueTriggers(ns.Name).Get(ctx, name, metav1.GetOptions{})
	require.NoErrorf(t, err, "GetMessageQueueTriggerConditions: get messagequeuetrigger %q", name)
	return r.Status.Conditions
}

// GetCanaryConfigConditions returns the Conditions slice on the named
// CanaryConfig's Status. CanaryConfig status is still written via the main
// resource (no subresource).
func (ns *TestNamespace) GetCanaryConfigConditions(t *testing.T, ctx context.Context, name string) []metav1.Condition {
	t.Helper()
	r, err := ns.f.fissionClient.CoreV1().CanaryConfigs(ns.Name).Get(ctx, name, metav1.GetOptions{})
	require.NoErrorf(t, err, "GetCanaryConfigConditions: get canaryconfig %q", name)
	return r.Status.Conditions
}

// GetEnvironmentConditions returns the Conditions slice on the named
// Environment's Status. No Fission controller writes these in this PR
// (see pkg/buildermgr/envwatcher.go.AddUpdateBuilder for the rationale)
// so this helper is retained for forward compatibility — it always
// returns nil today.
func (ns *TestNamespace) GetEnvironmentConditions(t *testing.T, ctx context.Context, name string) []metav1.Condition {
	t.Helper()
	r, err := ns.f.fissionClient.CoreV1().Environments(ns.Name).Get(ctx, name, metav1.GetOptions{})
	require.NoErrorf(t, err, "GetEnvironmentConditions: get environment %q", name)
	return r.Status.Conditions
}

// WaitForConditionTrue polls fetchConds until the named condition Type
// has Status=True or the timeout elapses. fetchConds is wrapped so tests
// can use the framework's existing per-CRD getters without naming the
// resource twice in the call site.
func WaitForConditionTrue(t *testing.T, ctx context.Context, what, condType string, timeout time.Duration, fetchConds func() []metav1.Condition) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		conds := fetchConds()
		cond := conditions.Find(conds, condType)
		if !assert.NotNilf(c, cond, "%s: condition %q not yet present (have: %v)", what, condType, condNames(conds)) {
			return
		}
		assert.Equalf(c, metav1.ConditionTrue, cond.Status,
			"%s: condition %q expected True, got %s (reason=%s message=%s)",
			what, condType, cond.Status, cond.Reason, cond.Message)
	}, timeout, 500*time.Millisecond)
}

// WaitForFunctionConditionReady polls until Function.Status.Conditions[Ready]
// is True or timeout fires. The executor (poolmgr / newdeploy / container)
// is the typical writer.
func (ns *TestNamespace) WaitForFunctionConditionReady(t *testing.T, ctx context.Context, name, condType string, timeout time.Duration) {
	t.Helper()
	WaitForConditionTrue(t, ctx, "function "+name, condType, timeout, func() []metav1.Condition {
		return ns.GetFunctionConditions(t, ctx, name)
	})
}

// WaitForPackageConditionTrue polls until Package.Status.Conditions[condType]
// is True or timeout fires. Useful for asserting PackageConditionBuildSucceeded
// or PackageConditionReady alongside the legacy BuildStatus poll in
// WaitForPackageBuildSucceeded.
func (ns *TestNamespace) WaitForPackageConditionTrue(t *testing.T, ctx context.Context, name, condType string, timeout time.Duration) {
	t.Helper()
	WaitForConditionTrue(t, ctx, "package "+name, condType, timeout, func() []metav1.Condition {
		return ns.GetPackageConditions(t, ctx, name)
	})
}

// condNames returns just the Type names from a Condition slice, for use in
// EventuallyWithT failure messages where the full struct dump is noisy.
func condNames(conds []metav1.Condition) []string {
	out := make([]string, 0, len(conds))
	for i := range conds {
		out = append(out, conds[i].Type)
	}
	return out
}
