// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package framework

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MessageQueueTriggerOptions are the inputs to
// TestNamespace.CreateMessageQueueTrigger.
type MessageQueueTriggerOptions struct {
	// Name of the MessageQueueTrigger CR. Required.
	Name string
	// Function the trigger delivers topic events to. Required.
	Function string
	// MQType selects the provider (e.g. "statestore", "kafka"). Required.
	MQType string
	// Topic the trigger consumes. Required.
	Topic string
	// MqtKind is "fission" (a classic per-type consumer head) or "keda".
	// Defaults to "fission" — the CLI's own default is "keda", which rejects
	// non-KEDA types like statestore.
	MqtKind string
	// ResponseTopic and ErrorTopic are optional publish-back topics.
	ResponseTopic string
	ErrorTopic    string
	// MaxRetries is the delivery retry budget (0 = deliver once).
	MaxRetries int
}

// CreateMessageQueueTrigger creates a MessageQueueTrigger CR via the CLI. The
// matching mqt head (mqtrigger-<type> for classic kinds) then delivers each
// topic event to Function on the router's internal listener. Cleanup deletes
// the trigger, which tears down the head's subscription.
func (ns *TestNamespace) CreateMessageQueueTrigger(t *testing.T, ctx context.Context, opts MessageQueueTriggerOptions) {
	t.Helper()
	require.NotEmpty(t, opts.Name, "MessageQueueTriggerOptions.Name")
	require.NotEmpty(t, opts.Function, "MessageQueueTriggerOptions.Function")
	require.NotEmpty(t, opts.MQType, "MessageQueueTriggerOptions.MQType")
	require.NotEmpty(t, opts.Topic, "MessageQueueTriggerOptions.Topic")
	kind := opts.MqtKind
	if kind == "" {
		kind = "fission"
	}

	args := []string{
		"mqt", "create",
		"--name", opts.Name,
		"--function", opts.Function,
		"--mqtype", opts.MQType,
		"--mqtkind", kind,
		"--topic", opts.Topic,
		"--maxretries", strconv.Itoa(opts.MaxRetries),
	}
	if opts.ResponseTopic != "" {
		args = append(args, "--resptopic", opts.ResponseTopic)
	}
	if opts.ErrorTopic != "" {
		args = append(args, "--errortopic", opts.ErrorTopic)
	}
	ns.CLI(t, ctx, args...)

	ns.addCleanup("messagequeuetrigger "+opts.Name, func(c context.Context) error {
		err := ns.f.fissionClient.CoreV1().MessageQueueTriggers(ns.Name).Delete(c, opts.Name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	})
}
