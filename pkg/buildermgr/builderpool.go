// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

const (
	// DefaultBuilderIdleTimeout is the idle window (in seconds) after which a
	// builder deployment with no in-flight builds is scaled to zero. A value of
	// 0 disables scale-to-zero ("keep the builder warm forever").
	DefaultBuilderIdleTimeout int64 = 600

	// DefaultBuilderPoolSize is the default ceiling on the number of builder
	// pods provisioned per environment for concurrent builds.
	DefaultBuilderPoolSize int32 = 1
)

// builderPoolSize returns the MAXIMUM number of builder pods allowed for an
// environment (spec.builder.poolsize). Pods are provisioned on demand up to this
// cap; it is not a fixed replica count. Defaults to DefaultBuilderPoolSize when
// unset or < 1, preserving the original single-builder behaviour.
func builderPoolSize(env *fv1.Environment) int32 {
	if env.Spec.Builder.PoolSize != nil && *env.Spec.Builder.PoolSize >= 1 {
		return *env.Spec.Builder.PoolSize
	}
	return DefaultBuilderPoolSize
}

// builderIdleTimeout returns the idle-timeout (seconds) for an environment
// (spec.builder.idleTimeout), defaulting to DefaultBuilderIdleTimeout when unset.
// A value of 0 is honoured and means "never scale to zero".
func builderIdleTimeout(env *fv1.Environment) int64 {
	if env.Spec.Builder.IdleTimeout != nil {
		return *env.Spec.Builder.IdleTimeout
	}
	return DefaultBuilderIdleTimeout
}

// buildKey identifies an in-flight package build within an environment.
type buildKey struct {
	namespace string
	name      string
}

// builderState is the per-environment build coordination state. Its own mutex
// guards every field; lock ordering is always BuilderPoolManager.mu before
// builderState.mu, never the reverse.
type builderState struct {
	mu            sync.Mutex
	envName       string
	envNamespace  string
	envRV         string
	builderNS     string
	builderName   string // "<envName>-<envRV>"
	idleTimeout   int64
	poolSize      int32
	inFlight      map[buildKey]struct{} // packages currently building (demand)
	busyPodIPs    map[string]bool       // builder pod IPs claimed by a build
	lastBuildTime time.Time
	scaledToZero  bool // set by the reaper; cleared when a build starts
}

// reapTarget identifies a builder deployment eligible for scale-to-zero.
type reapTarget struct {
	uid         types.UID
	builderNS   string
	builderName string
	envName     string
}

// BuilderPoolManager holds the in-memory, per-environment build coordination
// state shared by the EnvironmentReconciler, the PackageReconciler and the idle
// builder reaper: which builds are in flight (demand), which builder pod IPs are
// claimed, and when each environment last built (idle timing). It replaces the
// in-memory cache the pre-reconciler environmentWatcher used to own.
//
// All methods are safe for concurrent use.
type BuilderPoolManager struct {
	mu     sync.RWMutex
	states map[types.UID]*builderState
	logger logr.Logger
	now    func() time.Time // injectable clock for tests
}

func newBuilderPoolManager(logger logr.Logger) *BuilderPoolManager {
	return &BuilderPoolManager{
		states: make(map[types.UID]*builderState),
		logger: logger.WithName("builder_pool"),
		now:    time.Now,
	}
}

// getOrCreate returns the state for env.UID, creating it if absent, and refreshes
// the descriptive fields from the live Environment. When the builder deployment
// name changes (a new Environment generation), scaledToZero is cleared because a
// fresh deployment starts at one replica.
func (m *BuilderPoolManager) getOrCreate(env *fv1.Environment, builderNS string) *builderState {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.states[env.UID]
	if !ok {
		st = &builderState{
			inFlight:      make(map[buildKey]struct{}),
			busyPodIPs:    make(map[string]bool),
			lastBuildTime: m.now(),
		}
		m.states[env.UID] = st
	}
	name := fmt.Sprintf("%v-%v", env.Name, env.ResourceVersion)
	st.mu.Lock()
	if st.builderName != "" && st.builderName != name {
		st.scaledToZero = false
	}
	st.envName = env.Name
	st.envNamespace = env.Namespace
	st.envRV = env.ResourceVersion
	st.builderNS = builderNS
	st.builderName = name
	st.idleTimeout = builderIdleTimeout(env)
	st.poolSize = builderPoolSize(env)
	st.mu.Unlock()
	return st
}

func (m *BuilderPoolManager) get(uid types.UID) (*builderState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st, ok := m.states[uid]
	return st, ok
}

// Ensure registers/refreshes an environment's builder metadata. Idempotent;
// called by the EnvironmentReconciler each reconcile so the manager's view (and
// the reaper) stays current and repopulates after a buildermgr restart.
func (m *BuilderPoolManager) Ensure(env *fv1.Environment, builderNS string) {
	m.getOrCreate(env, builderNS)
}

// Forget drops an environment's state by UID (Environment deletion).
func (m *BuilderPoolManager) Forget(uid types.UID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.states, uid)
}

// ForgetByName drops state matching name+namespace. The delete reconcile request
// carries no UID, so the reaper/cleanup path resolves by identity instead.
func (m *BuilderPoolManager) ForgetByName(envNamespace, envName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for uid, st := range m.states {
		st.mu.Lock()
		match := st.envName == envName && st.envNamespace == envNamespace
		st.mu.Unlock()
		if match {
			delete(m.states, uid)
		}
	}
}

// StartBuild records that pkg is building for env and returns the current demand
// (the number of distinct in-flight builds). It is idempotent in pkg, so a
// reconcile requeue for the same package does not inflate demand. It refreshes
// lastBuildTime and clears scaledToZero so the reaper will not scale the builder
// down underneath the build.
func (m *BuilderPoolManager) StartBuild(env *fv1.Environment, builderNS string, pkg *fv1.Package) int32 {
	st := m.getOrCreate(env, builderNS)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.inFlight[buildKey{pkg.Namespace, pkg.Name}] = struct{}{}
	st.lastBuildTime = m.now()
	st.scaledToZero = false
	return int32(len(st.inFlight))
}

// FinishBuild records that pkg's build reached a terminal state, and refreshes
// lastBuildTime so the idle window starts from the last completed build.
func (m *BuilderPoolManager) FinishBuild(uid types.UID, pkg *fv1.Package) {
	st, ok := m.get(uid)
	if !ok {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.inFlight, buildKey{pkg.Namespace, pkg.Name})
	st.lastBuildTime = m.now()
}

// RemoveBuild drops a package from every environment's in-flight set. Used when
// a Package is deleted while it is requeue-waiting for a builder pod: the wait
// keeps it counted as demand until terminal, but a deletion is terminal without a
// known env UID, so its slot would otherwise leak (keeping a builder warm and
// blocking the idle reaper). A package builds for exactly one environment, so
// sweeping all of them is safe.
func (m *BuilderPoolManager) RemoveBuild(pkgNamespace, pkgName string) {
	key := buildKey{pkgNamespace, pkgName}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, st := range m.states {
		st.mu.Lock()
		if _, ok := st.inFlight[key]; ok {
			delete(st.inFlight, key)
			st.lastBuildTime = m.now()
		}
		st.mu.Unlock()
	}
}

// IsBuilding reports whether any build is currently in flight for the env.
func (m *BuilderPoolManager) IsBuilding(uid types.UID) bool {
	st, ok := m.get(uid)
	if !ok {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.inFlight) > 0
}

// ClaimFreeBuilderPod marks and returns the first candidate pod IP not already
// claimed by another in-flight build, or ("", false) if every candidate is busy
// (the caller should requeue and retry). The caller MUST release the IP with
// ReleaseBuilderPod when the build finishes.
func (m *BuilderPoolManager) ClaimFreeBuilderPod(uid types.UID, candidateIPs []string) (string, bool) {
	st, ok := m.get(uid)
	if !ok {
		return "", false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, ip := range candidateIPs {
		if ip == "" || st.busyPodIPs[ip] {
			continue
		}
		st.busyPodIPs[ip] = true
		return ip, true
	}
	return "", false
}

// ReleaseBuilderPod frees a builder pod IP previously claimed via ClaimFreeBuilderPod.
func (m *BuilderPoolManager) ReleaseBuilderPod(uid types.UID, podIP string) {
	st, ok := m.get(uid)
	if !ok {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.busyPodIPs, podIP)
}

// ReapTargets returns the builder deployments eligible for scale-to-zero: those
// with no in-flight builds, a positive idle timeout, an elapsed idle window, and
// not already scaled to zero by a previous sweep.
func (m *BuilderPoolManager) ReapTargets() []reapTarget {
	m.mu.RLock()
	defer m.mu.RUnlock()
	now := m.now()
	var targets []reapTarget
	for uid, st := range m.states {
		st.mu.Lock()
		idle := !st.scaledToZero &&
			len(st.inFlight) == 0 &&
			st.idleTimeout > 0 &&
			st.builderName != "" &&
			now.Sub(st.lastBuildTime) >= time.Duration(st.idleTimeout)*time.Second
		t := reapTarget{uid: uid, builderNS: st.builderNS, builderName: st.builderName, envName: st.envName}
		st.mu.Unlock()
		if idle {
			targets = append(targets, t)
		}
	}
	return targets
}

// MarkScaledToZero records that the reaper has scaled the env's builder to zero,
// so subsequent sweeps skip it until a new build clears the flag.
func (m *BuilderPoolManager) MarkScaledToZero(uid types.UID) {
	st, ok := m.get(uid)
	if !ok {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.scaledToZero = true
}
