//go:build integration

package framework

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
// Environment's Status.
func (ns *TestNamespace) GetEnvironmentConditions(t *testing.T, ctx context.Context, name string) []metav1.Condition {
	t.Helper()
	r, err := ns.f.fissionClient.CoreV1().Environments(ns.Name).Get(ctx, name, metav1.GetOptions{})
	require.NoErrorf(t, err, "GetEnvironmentConditions: get environment %q", name)
	return r.Status.Conditions
}
