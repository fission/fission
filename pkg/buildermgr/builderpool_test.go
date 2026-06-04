// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

func poolEnv(uid, name, rv string, idleTimeout *int64, poolSize *int32) *fv1.Environment {
	return &fv1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       "default",
			UID:             types.UID(uid),
			ResourceVersion: rv,
		},
		Spec: fv1.EnvironmentSpec{
			Builder: fv1.Builder{
				Image:       "builder:latest",
				IdleTimeout: idleTimeout,
				PoolSize:    poolSize,
			},
		},
	}
}

func poolPkg(name string) *fv1.Package {
	return &fv1.Package{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
}

func i64(v int64) *int64 { return &v }
func i32(v int32) *int32 { return &v }

func TestBuilderPoolDemandIsIdempotentPerPackage(t *testing.T) {
	m := newBuilderPoolManager(loggerfactory.GetLogger())
	env := poolEnv("u1", "go", "1", nil, i32(3))

	assert.Equal(t, int32(1), m.StartBuild(env, "default", poolPkg("a")), "first package -> demand 1")
	assert.Equal(t, int32(1), m.StartBuild(env, "default", poolPkg("a")), "same package re-entry must not inflate demand")
	assert.Equal(t, int32(2), m.StartBuild(env, "default", poolPkg("b")), "second distinct package -> demand 2")
	assert.True(t, m.IsBuilding(env.UID))

	m.FinishBuild(env.UID, poolPkg("a"))
	assert.True(t, m.IsBuilding(env.UID), "still one build in flight")
	m.FinishBuild(env.UID, poolPkg("b"))
	assert.False(t, m.IsBuilding(env.UID), "no builds left in flight")
}

func TestBuilderPoolRemoveBuildClearsLeakedDemand(t *testing.T) {
	m := newBuilderPoolManager(loggerfactory.GetLogger())
	env := poolEnv("u1", "go", "1", nil, i32(3))
	m.StartBuild(env, "default", poolPkg("a"))
	m.StartBuild(env, "default", poolPkg("b"))
	assert.True(t, m.IsBuilding(env.UID))

	// A package deleted while requeue-waiting must release its demand slot even
	// though the caller has no env UID to hand (RemoveBuild sweeps by identity).
	m.RemoveBuild("default", "a")
	assert.True(t, m.IsBuilding(env.UID), "one build still in flight")
	m.RemoveBuild("default", "b")
	assert.False(t, m.IsBuilding(env.UID), "demand fully released after both removed")
}

func TestBuilderPoolClaimAndRelease(t *testing.T) {
	m := newBuilderPoolManager(loggerfactory.GetLogger())
	env := poolEnv("u1", "go", "1", nil, i32(2))
	m.Ensure(env, "default")

	ip, ok := m.ClaimFreeBuilderPod(env.UID, []string{"", "10.0.0.1", "10.0.0.2"})
	assert.True(t, ok)
	assert.Equal(t, "10.0.0.1", ip, "must skip the empty IP and claim the first real one")

	ip2, ok := m.ClaimFreeBuilderPod(env.UID, []string{"10.0.0.1", "10.0.0.2"})
	assert.True(t, ok)
	assert.Equal(t, "10.0.0.2", ip2, "must skip the already-claimed IP")

	_, ok = m.ClaimFreeBuilderPod(env.UID, []string{"10.0.0.1", "10.0.0.2"})
	assert.False(t, ok, "all candidates busy -> no claim")

	m.ReleaseBuilderPod(env.UID, "10.0.0.1")
	ip3, ok := m.ClaimFreeBuilderPod(env.UID, []string{"10.0.0.1", "10.0.0.2"})
	assert.True(t, ok)
	assert.Equal(t, "10.0.0.1", ip3, "released IP becomes claimable again")
}

func TestBuilderPoolReapTargets(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	m := newBuilderPoolManager(loggerfactory.GetLogger())
	m.now = func() time.Time { return now }

	// idle, timeout elapsed -> reaped
	idle := poolEnv("idle", "idle", "1", i64(60), nil)
	m.Ensure(idle, "default")
	// building -> not reaped
	busy := poolEnv("busy", "busy", "1", i64(60), nil)
	m.StartBuild(busy, "default", poolPkg("p"))
	// idleTimeout 0 -> never reaped
	never := poolEnv("never", "never", "1", i64(0), nil)
	m.Ensure(never, "default")

	// advance the clock past the idle window for everything seeded so far
	now = now.Add(120 * time.Second)

	// recent build -> seeded AFTER the advance, so its lastBuildTime is "now" and
	// it is still inside its idle window
	recent := poolEnv("recent", "recent", "1", i64(60), nil)
	m.Ensure(recent, "default")

	targets := m.ReapTargets()
	names := map[string]bool{}
	for _, tg := range targets {
		names[tg.envName] = true
	}
	assert.True(t, names["idle"], "idle builder past its timeout must be a reap target")
	assert.False(t, names["busy"], "a building env must never be reaped")
	assert.False(t, names["never"], "idleTimeout=0 must never be reaped")
	assert.False(t, names["recent"], "an env built within its window must not be reaped")

	// once marked scaled-to-zero, idle is skipped on subsequent sweeps
	m.MarkScaledToZero(idle.UID)
	for _, tg := range m.ReapTargets() {
		assert.NotEqual(t, "idle", tg.envName, "already scaled-to-zero env must be skipped")
	}

	// a new build clears scaledToZero and refreshes the timer
	m.StartBuild(idle, "default", poolPkg("q"))
	m.FinishBuild(idle.UID, poolPkg("q"))
	now = now.Add(120 * time.Second)
	found := false
	for _, tg := range m.ReapTargets() {
		found = found || tg.envName == "idle"
	}
	assert.True(t, found, "after a fresh build + idle window, the env is reapable again")
}

func TestBuilderPoolForget(t *testing.T) {
	m := newBuilderPoolManager(loggerfactory.GetLogger())
	a := poolEnv("ua", "a", "1", i64(60), nil)
	b := poolEnv("ub", "b", "1", i64(60), nil)
	m.Ensure(a, "default")
	m.Ensure(b, "default")

	m.Forget(a.UID)
	_, ok := m.get(a.UID)
	assert.False(t, ok, "Forget removes state by UID")

	m.ForgetByName("default", "b")
	_, ok = m.get(b.UID)
	assert.False(t, ok, "ForgetByName removes state by name+namespace")
}

func TestBuilderPoolNewGenerationClearsScaledToZero(t *testing.T) {
	m := newBuilderPoolManager(loggerfactory.GetLogger())
	env := poolEnv("u1", "go", "1", i64(60), nil)
	m.Ensure(env, "default")
	m.MarkScaledToZero(env.UID)

	// a new Environment generation (new ResourceVersion) means a fresh builder
	// deployment at one replica, so the scaledToZero flag must reset.
	env2 := poolEnv("u1", "go", "2", i64(60), nil)
	m.Ensure(env2, "default")
	st, ok := m.get(env.UID)
	assert.True(t, ok)
	st.mu.Lock()
	assert.False(t, st.scaledToZero, "new generation must clear scaledToZero")
	assert.Equal(t, "go-2", st.builderName, "builderName must track the new RV")
	st.mu.Unlock()
}
