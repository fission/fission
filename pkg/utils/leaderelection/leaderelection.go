// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package leaderelection wraps client-go leader election with an "enabled"
// switch so callers can gate leader-only work behind Leading()/IsLeader()
// regardless of whether election is actually turned on. When disabled the
// process is treated as the sole leader immediately, preserving the historical
// single-replica behaviour byte-for-byte.
package leaderelection

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

const (
	defaultLeaseDuration = 15 * time.Second
	defaultRenewDeadline = 10 * time.Second
	defaultRetryPeriod   = 2 * time.Second

	saNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

// Elector manages (optional) leader election for a control-plane component.
type Elector struct {
	enabled bool
	logger  logr.Logger
	lock    resourcelock.Interface
	name    string

	leaseDuration time.Duration
	renewDeadline time.Duration
	retryPeriod   time.Duration

	isLeader  atomic.Bool
	leadingCh chan struct{}
	closeOnce sync.Once

	onStoppedLeading func()
}

// Option customizes an Elector.
type Option func(*Elector)

// WithOnStoppedLeading registers a callback invoked when leadership is lost
// (or could not be established). Callers typically use it to trigger a
// graceful shutdown so the pod restarts and rejoins as a standby.
func WithOnStoppedLeading(f func()) Option {
	return func(e *Elector) { e.onStoppedLeading = f }
}

// WithDurations overrides the lease/renew/retry timings (mainly for tests).
func WithDurations(lease, renew, retry time.Duration) Option {
	return func(e *Elector) {
		e.leaseDuration = lease
		e.renewDeadline = renew
		e.retryPeriod = retry
	}
}

// New builds an Elector. When enabled is false the returned Elector is a no-op
// that reports itself as the leader as soon as Run starts. When enabled, a
// Lease lock named lockName is contended in namespace, identified by identity.
func New(enabled bool, client kubernetes.Interface, namespace, lockName, identity string, logger logr.Logger, opts ...Option) *Elector {
	e := &Elector{
		enabled:       enabled,
		logger:        logger.WithName("leaderelection"),
		name:          lockName,
		leaseDuration: defaultLeaseDuration,
		renewDeadline: defaultRenewDeadline,
		retryPeriod:   defaultRetryPeriod,
		leadingCh:     make(chan struct{}),
	}
	for _, o := range opts {
		o(e)
	}
	if enabled {
		e.lock = &resourcelock.LeaseLock{
			LeaseMeta:  metav1.ObjectMeta{Name: lockName, Namespace: namespace},
			Client:     client.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{Identity: identity},
		}
	}
	return e
}

// IsLeader reports whether this process currently holds leadership. Always
// true once Run starts when election is disabled.
func (e *Elector) IsLeader() bool { return e.isLeader.Load() }

// Leading returns a channel that is closed the first time leadership is
// acquired. Gate leader-only goroutines on it: `<-elector.Leading()`.
func (e *Elector) Leading() <-chan struct{} { return e.leadingCh }

func (e *Elector) markLeading() {
	e.isLeader.Store(true)
	e.closeOnce.Do(func() { close(e.leadingCh) })
}

// Run blocks until ctx is cancelled. When election is disabled it marks
// leadership immediately and waits. When enabled it runs client-go leader
// election; on losing leadership the onStoppedLeading callback (if any) fires.
func (e *Elector) Run(ctx context.Context) {
	if !e.enabled {
		e.markLeading()
		<-ctx.Done()
		return
	}

	le, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:            e.lock,
		ReleaseOnCancel: true,
		LeaseDuration:   e.leaseDuration,
		RenewDeadline:   e.renewDeadline,
		RetryPeriod:     e.retryPeriod,
		Name:            e.name,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(context.Context) {
				e.logger.Info("acquired leadership")
				e.markLeading()
			},
			OnStoppedLeading: func() {
				e.isLeader.Store(false)
				e.logger.Info("lost leadership")
				if e.onStoppedLeading != nil {
					e.onStoppedLeading()
				}
			},
			OnNewLeader: func(identity string) {
				e.logger.V(1).Info("observed leader", "identity", identity)
			},
		},
	})
	if err != nil {
		e.logger.Error(err, "failed to create leader elector")
		if e.onStoppedLeading != nil {
			e.onStoppedLeading()
		}
		return
	}
	// Run blocks until leadership is lost or ctx is cancelled.
	le.Run(ctx)
}

// Namespace returns the namespace the current pod runs in, used as the home
// for the Lease object. It prefers POD_NAMESPACE (downward API) and falls back
// to the in-cluster service-account namespace file. Empty string if neither is
// available (caller should treat that as "leader election unavailable").
func Namespace() string {
	if ns := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); ns != "" {
		return ns
	}
	if data, err := os.ReadFile(saNamespaceFile); err == nil {
		if ns := strings.TrimSpace(string(data)); ns != "" {
			return ns
		}
	}
	return ""
}

// Identity returns a stable, unique identity for this process to use in the
// Lease. It prefers POD_NAME (downward API) and falls back to the hostname.
func Identity() string {
	if id := strings.TrimSpace(os.Getenv("POD_NAME")); id != "" {
		return id
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return "unknown"
}
