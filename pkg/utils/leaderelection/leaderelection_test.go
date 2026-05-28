// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package leaderelection

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestElectorDisabledIsImmediateLeader(t *testing.T) {
	e := New(false, nil, "ns", "lock", "id", logr.Discard())
	assert.False(t, e.IsLeader(), "should not be leader before Run")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()

	select {
	case <-e.Leading():
	case <-time.After(2 * time.Second):
		t.Fatal("disabled elector should become leader immediately")
	}
	assert.True(t, e.IsLeader())

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run should return after ctx cancel")
	}
}

func TestElectorEnabledAcquiresLeadership(t *testing.T) {
	client := fake.NewSimpleClientset()
	stopped := make(chan struct{})
	e := New(true, client, "fission", "fission-executor", "pod-a", logr.Discard(),
		WithDurations(2*time.Second, 1500*time.Millisecond, 300*time.Millisecond),
		WithOnStoppedLeading(func() { close(stopped) }),
	)

	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)

	select {
	case <-e.Leading():
	case <-time.After(10 * time.Second):
		t.Fatal("enabled elector should acquire uncontended leadership")
	}
	assert.True(t, e.IsLeader())

	// Lease should now exist in the configured namespace.
	lease, err := client.CoordinationV1().Leases("fission").Get(ctx, "fission-executor", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, lease.Spec.HolderIdentity)
	assert.Equal(t, "pod-a", *lease.Spec.HolderIdentity)

	// Cancelling releases leadership and fires onStoppedLeading.
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("onStoppedLeading should fire when leadership ends")
	}
	assert.False(t, e.IsLeader())
}

func TestMarkLeadingIsIdempotent(t *testing.T) {
	e := New(false, nil, "ns", "lock", "id", logr.Discard())
	assert.NotPanics(t, func() {
		e.markLeading()
		e.markLeading()
	})
	assert.True(t, e.IsLeader())
}
