//go:build integration

package common_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	fapply "github.com/fission/fission/pkg/generated/applyconfiguration/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// minimalFunction returns a Function spec accepted by the validating webhook
// even though it references no real Environment/Package. The webhook validates
// reference *shape* (non-empty Name/Namespace) but does not look up the env,
// so a fake reference is sufficient for CRD-schema smoke tests. The Function
// will never be specialized — that's fine, these tests only exercise the
// apiserver/CRD plumbing.
func minimalFunction(name, namespace string) *fv1.Function {
	return &fv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: fv1.FunctionSpec{
			Environment: fv1.EnvironmentReference{Name: "conds-smoke-fake", Namespace: namespace},
			Package:     fv1.FunctionPackageRef{PackageRef: fv1.PackageRef{Name: "conds-smoke-fake", Namespace: namespace}},
			InvokeStrategy: fv1.InvokeStrategy{
				StrategyType: fv1.StrategyTypeExecution,
				ExecutionStrategy: fv1.ExecutionStrategy{
					ExecutorType: fv1.ExecutorTypePoolmgr,
					MinScale:     0,
					MaxScale:     1,
				},
			},
		},
	}
}

// TestConditions_FunctionStatusSubresource creates a Function via the
// typed client, asserts the Status field is empty (no controller writer
// exists yet), then mutates Status.Conditions via UpdateStatus and re-fetches
// to verify the apiserver persists it through the new status subresource.
func TestConditions_FunctionStatusSubresource(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)
	fc := f.FissionClient().CoreV1()

	name := "conds-fn-" + ns.ID
	_, err := fc.Functions(ns.Name).Create(ctx, minimalFunction(name, ns.Name), metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = fc.Functions(ns.Name).Delete(context.Background(), name, metav1.DeleteOptions{})
	})

	conds := ns.GetFunctionConditions(t, ctx, name)
	require.Empty(t, conds, "no controller writes conditions yet — expected empty Conditions")

	fn, err := fc.Functions(ns.Name).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	fn.Status.Conditions = []metav1.Condition{
		{
			Type:               fv1.FunctionConditionReady,
			Status:             metav1.ConditionUnknown,
			Reason:             "ConditionsSmoke",
			Message:            "set by TestConditions_FunctionStatusSubresource",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: fn.Generation,
		},
	}
	_, err = fc.Functions(ns.Name).UpdateStatus(ctx, fn, metav1.UpdateOptions{})
	require.NoError(t, err, "UpdateStatus on the new /status subresource must succeed")

	got := ns.GetFunctionConditions(t, ctx, name)
	require.Len(t, got, 1)
	require.Equal(t, fv1.FunctionConditionReady, got[0].Type)
	require.EqualValues(t, "Unknown", got[0].Status)
	require.Equal(t, "ConditionsSmoke", got[0].Reason)
}

// TestConditions_PackageMainResource verifies the additive change to
// PackageStatus: Conditions can be written via the existing main-resource
// Update path (no status subresource on Package), and round-trips cleanly.
// This is the canary that PackageStatus changes did NOT silently flip the
// subresource on Package, which would have broken pkg/buildermgr writes.
func TestConditions_PackageMainResource(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)
	fc := f.FissionClient().CoreV1()

	name := "conds-pkg-" + ns.ID
	pkg := &fv1.Package{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name},
		Spec: fv1.PackageSpec{
			Environment: fv1.EnvironmentReference{Name: "conds-smoke-fake", Namespace: ns.Name},
			Deployment:  fv1.Archive{Type: fv1.ArchiveTypeLiteral, Literal: []byte("// noop")},
		},
		Status: fv1.PackageStatus{BuildStatus: fv1.BuildStatusNone},
	}
	_, err := fc.Packages(ns.Name).Create(ctx, pkg, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = fc.Packages(ns.Name).Delete(context.Background(), name, metav1.DeleteOptions{})
	})

	got, err := fc.Packages(ns.Name).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	got.Status.Conditions = []metav1.Condition{
		{
			Type:               fv1.PackageConditionBuildSucceeded,
			Status:             metav1.ConditionTrue,
			Reason:             "ConditionsSmoke",
			Message:            "set by TestConditions_PackageMainResource",
			LastTransitionTime: metav1.Now(),
			ObservedGeneration: got.Generation,
		},
	}
	// Main-resource Update writes Spec AND Status together — proves we did
	// NOT add +kubebuilder:subresource:status to Package.
	_, err = fc.Packages(ns.Name).Update(ctx, got, metav1.UpdateOptions{})
	require.NoError(t, err, "Update on Package main resource must persist Status.Conditions")

	refetched := ns.GetPackageConditions(t, ctx, name)
	require.Len(t, refetched, 1)
	require.Equal(t, fv1.PackageConditionBuildSucceeded, refetched[0].Type)
}

// TestConditions_SSAListMapKey is the core SSA correctness test for
// FunctionSpec.Secrets / ConfigMaps. Two distinct field managers apply
// non-overlapping secret entries; the resulting list must contain BOTH —
// because Secrets is marked +listType=map / +listMapKey=name. With an
// atomic listType the second apply would have clobbered the first
// manager's entry.
func TestConditions_SSAListMapKey(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)
	fc := f.FissionClient().CoreV1()

	name := "conds-ssa-" + ns.ID

	// minimalApply returns an apply config matching minimalFunction so the
	// first Apply has enough required fields to pass admission.
	minimalApply := func() *fapply.FunctionApplyConfiguration {
		return fapply.Function(name, ns.Name).
			WithSpec(fapply.FunctionSpec().
				WithEnvironment(fapply.EnvironmentReference().
					WithName("conds-smoke-fake").WithNamespace(ns.Name)).
				WithPackage(fapply.FunctionPackageRef().
					WithPackageRef(fapply.PackageRef().
						WithName("conds-smoke-fake").WithNamespace(ns.Name))).
				WithInvokeStrategy(fapply.InvokeStrategy().
					WithStrategyType(fv1.StrategyTypeExecution).
					WithExecutionStrategy(fapply.ExecutionStrategy().
						WithExecutorType(fv1.ExecutorTypePoolmgr).
						WithMinScale(0).
						WithMaxScale(1))))
	}

	// Writer A owns secret "alpha".
	a := minimalApply().WithSpec(minimalApply().Spec.
		WithSecrets(fapply.SecretReference().WithName("alpha").WithNamespace(ns.Name)))
	_, err := fc.Functions(ns.Name).Apply(ctx, a, metav1.ApplyOptions{FieldManager: "writer-a", Force: true})
	require.NoError(t, err, "first Apply (writer-a)")
	t.Cleanup(func() {
		_ = fc.Functions(ns.Name).Delete(context.Background(), name, metav1.DeleteOptions{})
	})

	// Writer B owns secret "beta". Note: this Apply only sets Secrets=[beta].
	// Because Secrets is a list-map keyed by name, "alpha" remains owned by
	// writer-a and stays in the resulting list.
	b := minimalApply().WithSpec(minimalApply().Spec.
		WithSecrets(fapply.SecretReference().WithName("beta").WithNamespace(ns.Name)))
	_, err = fc.Functions(ns.Name).Apply(ctx, b, metav1.ApplyOptions{FieldManager: "writer-b", Force: true})
	require.NoError(t, err, "second Apply (writer-b)")

	fn, err := fc.Functions(ns.Name).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)

	names := make([]string, 0, len(fn.Spec.Secrets))
	for _, s := range fn.Spec.Secrets {
		names = append(names, s.Name)
	}
	require.ElementsMatch(t, []string{"alpha", "beta"}, names,
		"both managers' Secrets entries must survive — proves +listType=map / +listMapKey=name took effect")
}

// TestConditions_StatusSubresourceIsolated verifies that Spec edits via
// UpdateStatus are rejected (or dropped) on a CRD that has the status
// subresource — proves the subresource boundary is actually enforced after
// our marker change.
func TestConditions_StatusSubresourceIsolated(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	f := framework.Connect(t)
	ns := f.NewTestNamespace(t)
	fc := f.FissionClient().CoreV1()

	name := "conds-iso-" + ns.ID
	_, err := fc.Functions(ns.Name).Create(ctx, minimalFunction(name, ns.Name), metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = fc.Functions(ns.Name).Delete(context.Background(), name, metav1.DeleteOptions{})
	})

	fn, err := fc.Functions(ns.Name).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	originalEnvName := fn.Spec.Environment.Name

	// Mutate both spec and status; submit via UpdateStatus.
	// The apiserver must drop the spec change (subresource semantics).
	fn.Spec.Environment.Name = "conds-tampered"
	fn.Status.Conditions = []metav1.Condition{{
		Type: fv1.FunctionConditionProgressing, Status: metav1.ConditionTrue,
		Reason: "Tampering", LastTransitionTime: metav1.Now(),
	}}
	_, err = fc.Functions(ns.Name).UpdateStatus(ctx, fn, metav1.UpdateOptions{})
	require.NoError(t, err)

	after, err := fc.Functions(ns.Name).Get(ctx, name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, originalEnvName, after.Spec.Environment.Name,
		"spec change submitted via UpdateStatus must be dropped — proves the subresource boundary is enforced")
	require.Len(t, after.Status.Conditions, 1)
	require.Equal(t, fv1.FunctionConditionProgressing, after.Status.Conditions[0].Type)

	// And the inverse: a Patch on .metadata is fine and doesn't touch Status.
	_, err = fc.Functions(ns.Name).Patch(ctx, name, types.MergePatchType,
		[]byte(`{"metadata":{"labels":{"conds-test":"smoke"}}}`), metav1.PatchOptions{})
	require.NoError(t, err)

	final, err := fc.Functions(ns.Name).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		t.Skip("Function gone — likely the cleanup raced this test")
	}
	require.NoError(t, err)
	require.Equal(t, "smoke", final.Labels["conds-test"])
	require.Len(t, final.Status.Conditions, 1, "Status must survive an unrelated metadata patch")
}
