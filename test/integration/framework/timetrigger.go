// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package framework

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TimeTriggerOptions are the inputs to TestNamespace.CreateTimeTrigger.
type TimeTriggerOptions struct {
	// Name of the TimeTrigger CR. Required.
	Name string
	// Function the trigger invokes on its schedule. Required.
	Function string
	// Cron is the schedule (e.g. "@every 15s", "* * * * *"). Required.
	Cron string
}

// CreateTimeTrigger creates a TimeTrigger CR via the CLI. The timer subsystem
// then invokes Function on the router's internal listener every time the cron
// schedule fires. Cleanup deletes the TimeTrigger.
func (ns *TestNamespace) CreateTimeTrigger(t *testing.T, ctx context.Context, opts TimeTriggerOptions) {
	t.Helper()
	require.NotEmpty(t, opts.Name, "TimeTriggerOptions.Name")
	require.NotEmpty(t, opts.Function, "TimeTriggerOptions.Function")
	require.NotEmpty(t, opts.Cron, "TimeTriggerOptions.Cron")

	ns.CLI(t, ctx, "tt", "create", "--name", opts.Name, "--function", opts.Function, "--cron", opts.Cron)

	ns.addCleanup("timetrigger "+opts.Name, func(c context.Context) error {
		err := ns.f.fissionClient.CoreV1().TimeTriggers(ns.Name).Delete(c, opts.Name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	})
}
