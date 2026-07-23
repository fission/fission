// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package common_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// TestCanaryAliasPromotion and TestCanaryAliasRollback are the alias-mode
// counterparts of TestCanary (canary_test.go): the same real-traffic,
// real-Prometheus, 30s-increment shape, but the HTTPTrigger routes through a
// FunctionAlias rather than HTTPTrigger.FunctionWeights, driving
// pkg/canaryconfigmgr's alias-mode shim (RFC-0025 phase 5, stepAlias /
// rollForwardAlias / rollbackAlias) instead of its function-pair path.
//
// ROLE MAPPING under test (docs/rfc/0025-function-versions-aliases-rollback.md
// "CanaryConfig absorption", pkg/canaryconfigmgr/canaryConfigMgr.go's
// stepAlias doc comment): CanaryConfigSpec.OldFunction stays the alias's
// PRIMARY (Spec.Version) for the whole rollout; NewFunction is the
// SecondaryVersion. Spec.Weight (the primary's share) steps DOWN from 100 by
// WeightIncrement each interval. Spec.Version only ever changes on the
// terminal SUCCESS write -- that is the shim's one write that can produce an
// AliasReconciler Status.History append; the terminal FAILURE write leaves
// Spec.Version at OldFunction (unchanged) and so appends nothing.
//
// Both tests use aliasFixture/publishTwoVersions from alias_routing_test.go
// (same package) for the env/fn/publish-v1/update/publish-v2 skeleton, and
// startBackgroundLoad from canary_test.go for sustained router traffic.

// TestCanaryAliasPromotion mirrors TestCanary's "success" subtest: v2 answers
// 2xx throughout, so the controller steps the alias's primary weight down to
// 0 and performs the terminal promotion write -- {Version: v2, Weight: nil,
// SecondaryVersion: ""} -- appending exactly ONE Status.History record (for
// the outgoing OLD version, v1).
func TestCanaryAliasPromotion(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)
	af := newAliasFixture(t, f, "canaryaliasp", "canary")
	af.publishTwoVersions(t, ctx, image,
		framework.FunctionOptions{Code: writeNodeReturning(t, "v1", "hello, world!\n")},
		"--code", writeNodeReturning(t, "v2", "hello, world!\n"))

	// Start-state precondition (canaryConfigMgr.validateAliasRollout): the
	// alias must already point at OldFunction (v1) before the CanaryConfig is
	// created -- the shim's role mapping is a precondition, not something it
	// establishes for you.
	af.createAlias(t, ctx, af.V1Name)
	af.createRoute(t, ctx, http.MethodGet)

	// Make sure the alias-routed HTTPTrigger actually serves 2xx before the
	// canary kicks in -- gives a clean failure if the route/alias isn't ready.
	f.Router(t).GetEventually(t, ctx, af.RoutePath, framework.BodyContains("hello"))

	fc := f.FissionClient().CoreV1()
	aliasBefore, err := fc.FunctionAliases(af.ns.Name).Get(ctx, af.AliasName, metav1.GetOptions{})
	require.NoError(t, err)
	historyBefore := len(aliasBefore.Status.History)

	canaryName := "canary-" + af.ns.ID
	af.ns.CreateCanaryConfig(t, ctx, framework.CanaryConfigOptions{
		Name:              canaryName,
		NewFunction:       af.V2Name,
		OldFunction:       af.V1Name,
		HTTPTrigger:       af.RouteName,
		IncrementStep:     50,
		IncrementInterval: "30s",
		FailureThreshold:  10,
	})

	startBackgroundLoad(t, ctx, f, af.RoutePath)

	// Terminal state: alias fully promoted to v2, weight/secondary cleared.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		alias, err := fc.FunctionAliases(af.ns.Name).Get(ctx, af.AliasName, metav1.GetOptions{})
		if !assert.NoError(c, err) {
			return
		}
		assert.Equalf(c, af.V2Name, alias.Spec.Version, "primary must be promoted to the new version")
		assert.Nilf(c, alias.Spec.Weight, "weight must be cleared on the terminal promotion write")
		assert.Emptyf(c, alias.Spec.SecondaryVersion, "secondary version must be cleared on the terminal promotion write")
	}, 5*time.Minute, 2*time.Second)

	aliasAfter, err := fc.FunctionAliases(af.ns.Name).Get(ctx, af.AliasName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Lenf(t, aliasAfter.Status.History, historyBefore+1,
		"promotion must append exactly ONE new History record (the outgoing old version), got %+v", aliasAfter.Status.History)
	assert.Equalf(t, af.V1Name, aliasAfter.Status.History[len(aliasAfter.Status.History)-1].Version,
		"the appended History record must be for the outgoing OLD version")

	cfg, err := fc.CanaryConfigs(af.ns.Name).Get(ctx, canaryName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equalf(t, fv1.CanaryConfigStatusSucceeded, cfg.Status.Status,
		"canary config must reach terminal Succeeded status")
}

// TestCanaryAliasRollback mirrors TestCanary's "rollback" subtest: v2 answers
// 500 throughout, so once the controller has stepped a nonzero share of
// traffic onto it, the observed failure rate crosses FailureThreshold and the
// controller rolls back -- {Version: v1 (unchanged), Weight: nil,
// SecondaryVersion: ""} -- WITHOUT ever repointing Spec.Version, so it
// appends ZERO new Status.History records.
func TestCanaryAliasRollback(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	f := framework.Connect(t)
	image := f.Images().RequireNode(t)
	af := newAliasFixture(t, f, "canaryaliasrb", "canary")
	af.publishTwoVersions(t, ctx, image,
		framework.FunctionOptions{Code: writeNodeReturning(t, "v1", "hello, world!\n")},
		"--code", writeNodeStatus(t, "v2fail", http.StatusInternalServerError, "canary-alias-rb-v2", "boom\n"))

	// Start-state precondition: the alias must already point at OldFunction
	// (v1) before the CanaryConfig is created.
	af.createAlias(t, ctx, af.V1Name)
	af.createRoute(t, ctx, http.MethodGet)

	f.Router(t).GetEventually(t, ctx, af.RoutePath, framework.BodyContains("hello"))

	fc := f.FissionClient().CoreV1()
	aliasBefore, err := fc.FunctionAliases(af.ns.Name).Get(ctx, af.AliasName, metav1.GetOptions{})
	require.NoError(t, err)
	historyBefore := len(aliasBefore.Status.History)

	canaryName := "canary-rb-" + af.ns.ID
	af.ns.CreateCanaryConfig(t, ctx, framework.CanaryConfigOptions{
		Name:              canaryName,
		NewFunction:       af.V2Name,
		OldFunction:       af.V1Name,
		HTTPTrigger:       af.RouteName,
		IncrementStep:     50,
		IncrementInterval: "30s",
		FailureThreshold:  10,
	})

	startBackgroundLoad(t, ctx, f, af.RoutePath)

	// The failure threshold is measured *during a tick where v2 actually
	// receives traffic*. With the initial primary weight 100 (no traffic on
	// the secondary), the controller has to first step down (e.g. to 50)
	// before failures can register. Wait for that first step to confirm the
	// canary is alive -- otherwise a static, never-stepping controller
	// (broken) would vacuously pass the rollback check below.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		alias, err := fc.FunctionAliases(af.ns.Name).Get(ctx, af.AliasName, metav1.GetOptions{})
		if !assert.NoError(c, err) {
			return
		}
		if !assert.NotNilf(c, alias.Spec.Weight, "primary weight must have stepped down at least once") {
			return
		}
		assert.Lessf(c, *alias.Spec.Weight, 100, "primary weight must have stepped down at least once")
	}, 2*time.Minute, 2*time.Second)

	// Now wait for the controller to observe the failures and roll back fully.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		alias, err := fc.FunctionAliases(af.ns.Name).Get(ctx, af.AliasName, metav1.GetOptions{})
		if !assert.NoError(c, err) {
			return
		}
		assert.Equalf(c, af.V1Name, alias.Spec.Version, "primary must remain (or return to) the OLD version")
		assert.Nilf(c, alias.Spec.Weight, "weight must be cleared on rollback")
		assert.Emptyf(c, alias.Spec.SecondaryVersion, "secondary version must be cleared on rollback")
	}, 5*time.Minute, 2*time.Second)

	aliasAfter, err := fc.FunctionAliases(af.ns.Name).Get(ctx, af.AliasName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Lenf(t, aliasAfter.Status.History, historyBefore,
		"rollback must NOT append any new History record -- the primary's Spec.Version never actually changed, got %+v", aliasAfter.Status.History)

	cfg, err := fc.CanaryConfigs(af.ns.Name).Get(ctx, canaryName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equalf(t, fv1.CanaryConfigStatusFailed, cfg.Status.Status,
		"canary config must reach terminal Failed status")
}
