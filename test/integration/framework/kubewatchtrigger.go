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

// KubernetesWatchTriggerOptions are the inputs to
// TestNamespace.CreateKubernetesWatchTrigger.
type KubernetesWatchTriggerOptions struct {
	// Name of the KubernetesWatchTrigger CR. Required.
	Name string
	// Function invoked when a watched object changes. Required.
	Function string
	// ObjType is the resource kind to watch (CLI `--type`, e.g. "configmap",
	// "pod"). Defaults to the CLI default ("pod") when empty.
	ObjType string
	// WatchNamespace is the namespace whose objects are watched (CLI
	// `--namespace`). Defaults to the function namespace when empty.
	WatchNamespace string
}

// CreateKubernetesWatchTrigger creates a KubernetesWatchTrigger via the CLI.
// The kubewatcher subsystem watches the given resource type in WatchNamespace
// and invokes Function on each add/update/delete event. Cleanup deletes the
// trigger.
func (ns *TestNamespace) CreateKubernetesWatchTrigger(t *testing.T, ctx context.Context, opts KubernetesWatchTriggerOptions) {
	t.Helper()
	require.NotEmpty(t, opts.Name, "KubernetesWatchTriggerOptions.Name")
	require.NotEmpty(t, opts.Function, "KubernetesWatchTriggerOptions.Function")

	args := []string{"watch", "create", "--name", opts.Name, "--function", opts.Function}
	if opts.ObjType != "" {
		args = append(args, "--type", opts.ObjType)
	}
	if opts.WatchNamespace != "" {
		args = append(args, "--namespace", opts.WatchNamespace)
	}
	ns.CLI(t, ctx, args...)

	ns.addCleanup("kuberneteswatchtrigger "+opts.Name, func(c context.Context) error {
		err := ns.f.fissionClient.CoreV1().KubernetesWatchTriggers(ns.Name).Delete(c, opts.Name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	})
}
