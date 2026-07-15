// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package serial_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/integration/framework"
)

// requireEventingEnabled skips the test unless the built-in statestore MQ head
// (RFC-0027, chart gate statestore.enabled && eventing.enabled) is deployed.
func requireEventingEnabled(t *testing.T, ctx context.Context, f *framework.Framework) {
	t.Helper()
	_, err := f.KubeClient().AppsV1().Deployments(f.FissionNamespace()).Get(ctx, "mqtrigger-statestore", metav1.GetOptions{})
	if err != nil {
		t.Skipf("statestore eventing head is not deployed (mqtrigger-statestore: %v); skipping", err)
	}
}

// TestStatestoreEventingPipeline: the RFC-0027 zero-broker eventing loop
// end-to-end — an async invocation of function A publishes its result envelope
// to a statestore topic (onSuccess topic destination), and a
// messageQueueType: statestore MessageQueueTrigger consumes the topic and
// delivers the envelope to function B. No external broker anywhere.
func TestStatestoreEventingPipeline(t *testing.T) {
	f := framework.Connect(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	requireAsyncEnabled(t, ctx, f)
	requireEventingEnabled(t, ctx, f)
	image := f.Images().RequireNode(t)
	ns := f.NewTestNamespace(t)

	env := "node-evt-" + ns.ID
	srcFn := "evt-src-" + ns.ID
	dstFn := "evt-dst-" + ns.ID
	route := "/evt-src-" + ns.ID
	topic := "evt-orders-" + ns.ID
	mqtName := "evt-mqt-" + ns.ID

	ns.CreateEnv(t, ctx, framework.EnvOptions{Name: env, Image: image})
	// A logs a fixed marker; B logs its request body, so the result envelope's
	// functionRef ("<ns>/<srcFn>", unique per test run) is assertable in B's
	// logs with a churn-insensitive Contains.
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: srcFn, Env: env, Code: framework.WriteTestData(t, "nodejs/log/log.js"), ExecutorType: "poolmgr"})
	ns.CreateFunction(t, ctx, framework.FunctionOptions{Name: dstFn, Env: env, Code: framework.WriteTestData(t, "nodejs/log/logbody.js"), ExecutorType: "poolmgr"})
	ns.CreateRoute(t, ctx, framework.RouteOptions{Function: srcFn, URL: route, Method: http.MethodPost})
	ns.CreateMessageQueueTrigger(t, ctx, framework.MessageQueueTriggerOptions{
		Name:       mqtName,
		Function:   dstFn,
		MQType:     string(fv1.MessageQueueTypeStatestore),
		Topic:      topic,
		MaxRetries: 2,
	})

	setInvocation(t, ctx, f, ns, srcFn, &fv1.InvocationConfig{
		OnSuccess: &fv1.DestinationRef{Topic: &fv1.TopicRef{
			MessageQueueType: fv1.MessageQueueTypeStatestore,
			Topic:            topic,
		}},
	})

	// The statestore head marks the trigger BindingReady once its consumer loop
	// is subscribed — events published before that are legitimately unseen
	// (start-at-head subscription).
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		conds := ns.GetMessageQueueTriggerConditions(t, ctx, mqtName)
		assert.True(c, meta.IsStatusConditionTrue(conds, fv1.MessageQueueTriggerConditionBindingReady),
			"statestore head must subscribe the trigger")
	}, 2*time.Minute, 2*time.Second)

	warmRoute(t, ctx, f, route)
	warmInternal(t, ctx, f, dstFn)

	// Fire async invocations until the pipeline visibly completes. Retrying the
	// publish (fresh invocation per poll, no dedup key) makes the test immune to
	// the ms-scale race between BindingReady and the consumer's start-at-head
	// cursor snapshot; the Contains assertion is idempotent under the extra
	// deliveries at-least-once implies anyway. Everything inside the closure is
	// non-fatal (asyncPostE/FunctionLogsE) so a transient apiserver or router
	// hiccup is a retried tick, not a test failure from inside the retry loop.
	marker := ns.Name + "/" + srcFn
	require.Eventually(t, func() bool {
		status, _, err := asyncPostE(t, ctx, f, route, "")
		if err != nil || status != http.StatusAccepted {
			t.Logf("async post: status=%d err=%v (retrying)", status, err)
			return false
		}
		logs, err := ns.FunctionLogsE(t, ctx, dstFn)
		if err != nil {
			t.Logf("destination logs: %v (retrying)", err)
			return false
		}
		return strings.Contains(logs, marker)
	}, 4*time.Minute, 5*time.Second,
		"the result envelope must flow src → topic → statestore mqt → dst (marker %q in %s logs)", marker, dstFn)
}
