// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package scenario

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/test/benchmark/pkg/harness"
	"github.com/fission/fission/test/benchmark/pkg/loadgen"
	"github.com/fission/fission/test/benchmark/pkg/report"
)

// PromQL for the RFC-0027 eventing signals (summed across mqt-head replicas).
const eventingDeliveredQuery = `sum(fission_eventing_delivered_total{condition="success"})`

// eventingTopic measures the RFC-0027 zero-broker eventing loop: publishes are
// closed-loop POSTs to the router's topic admin API (durable EventLog append —
// E1 — so publish_* is the caller-visible publish overhead), and consumption is
// the statestore mqt head delivering each event to a consumer function. Metric
// families:
//   - publish_*        : durable topic-append latency/throughput under load
//   - consume_seconds  : time for the consumer to work through the published
//     backlog after load stops (via the delivered counter)
//   - consume_throughput: events/s the head delivered during that window
//
// It skips when the statestore mqt head is not deployed (statestore.enabled &&
// eventing.enabled off) and the consume_* family is best-effort (Prometheus).
// OFF the smoke subset — it needs a drain window; weekly/dispatch only.
type eventingTopic struct {
	duration    time.Duration
	warmup      time.Duration
	concurrency int
	poolsize    int
}

func (e *eventingTopic) Name() string   { return "eventing-topic" }
func (e *eventingTopic) Tags() []string { return []string{"eventing", "throughput", "queue"} }

func (e *eventingTopic) Run(ctx context.Context, sc *harness.Scope) (report.ScenarioResult, error) {
	var res report.ScenarioResult
	env := sc.Env()

	// The consuming head must be deployed, or the trigger below never binds.
	if _, err := env.Clients.Kube.AppsV1().Deployments(env.FissionNamespace()).
		Get(ctx, "mqtrigger-statestore", metav1.GetOptions{}); err != nil {
		return res, skip("statestore eventing head is not deployed (mqtrigger-statestore absent)")
	}

	// Consumer function: node multi-request-per-pod, pre-warmed so delivery is
	// never a cold start.
	_, fnName, err := provisionWarmFunction(ctx, sc, fv1.ExecutorTypePoolmgr, runtimeNode, e.poolsize, e.concurrency+10, []string{http.MethodPost})
	if err != nil {
		return res, err
	}
	res.SetMeta("function", fnName)

	topic := "bench-evt-" + fnName // fnName already carries the run-unique suffix
	mqtName := "bench-evt-" + fnName
	mqt := &fv1.MessageQueueTrigger{
		ObjectMeta: metav1.ObjectMeta{Name: mqtName, Namespace: env.Namespace},
		Spec: fv1.MessageQueueTriggerSpec{
			FunctionReference: fv1.FunctionReference{Type: fv1.FunctionReferenceTypeFunctionName, Name: fnName},
			MessageQueueType:  fv1.MessageQueueTypeStatestore,
			MqtKind:           "fission",
			Topic:             topic,
			MaxRetries:        1,
			ContentType:       "application/json",
		},
	}
	if _, err := env.Clients.Fission.CoreV1().MessageQueueTriggers(env.Namespace).Create(ctx, mqt, metav1.CreateOptions{}); err != nil {
		return res, fmt.Errorf("creating messagequeuetrigger: %w", err)
	}
	sc.Defer("messagequeuetrigger "+mqtName, func(c context.Context) error {
		err := env.Clients.Fission.CoreV1().MessageQueueTriggers(env.Namespace).Delete(c, mqtName, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	})

	// The head marks BindingReady once its consumer loop subscribed; publishes
	// before that are legitimately unseen (start-at-head).
	if err := waitMQTriggerBound(ctx, env, mqtName, 2*time.Minute); err != nil {
		return res, err
	}

	// Baseline the delivered counter so consume_* reflects only this run. The
	// counter is a CLUSTER-GLOBAL sum (no trigger label), so require it stable
	// across two reads first — a prior scenario's backlog still draining would
	// otherwise inflate the delta and understate consume_seconds. Residual
	// assumption: no OTHER statestore trigger delivers during the run itself
	// (true for the dedicated bench cluster this suite targets).
	waitCounterStable(ctx, env, eventingDeliveredQuery, 30*time.Second)
	deliveredBefore, deliveredBaselineOK := asyncQueryValue(ctx, env, eventingDeliveredQuery)

	q := url.Values{"namespace": {env.Namespace}, "topic": {topic}, "mqtype": {fv1.MessageQueueTypeStatestore}}
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	target := env.InternalAPITarget("/v1/eventing/topic/publish?"+q.Encode(), http.MethodPost,
		[]byte(`{"bench":true}`), headers, e.concurrency)
	r := loadgen.RunClosedLoop(ctx, loadgen.ClosedLoopConfig{
		Doer:        target.Do,
		Concurrency: e.concurrency,
		WarmUp:      e.warmup,
		Duration:    e.duration,
	})
	// publish_* is the durable-append latency: the 200 returns after the event
	// is appended (E1), so this is the caller-visible publish overhead.
	latencyMetrics(&res, "publish_", r)

	// consume_* : after publishing stops, watch the delivered counter climb to
	// the published count. Best-effort — needs Prometheus scraping the head.
	if env.Capturer.PrometheusEnabled() && deliveredBaselineOK {
		published := r.Total - r.Errors
		if secs, ok := eventingConsumeSeconds(ctx, env, deliveredBefore, float64(published), 5*time.Minute); ok {
			res.Add("consume_seconds", "s", report.Lower, secs)
			if secs > 0 {
				res.Add("consume_throughput", "rps", report.Higher, float64(published)/secs)
			}
		}
	}
	return res, nil
}

// waitMQTriggerBound polls the trigger's BindingReady condition.
func waitMQTriggerBound(ctx context.Context, env *harness.Env, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		mqt, err := env.Clients.Fission.CoreV1().MessageQueueTriggers(env.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil && meta.IsStatusConditionTrue(mqt.Status.Conditions, fv1.MessageQueueTriggerConditionBindingReady) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("messagequeuetrigger %s never reached BindingReady within %s", name, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// waitCounterStable polls until two consecutive reads of query are equal (a
// prior workload has drained) or the timeout elapses; best-effort.
func waitCounterStable(ctx context.Context, env *harness.Env, query string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	prev, ok := asyncQueryValue(ctx, env, query)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
		cur, ok2 := asyncQueryValue(ctx, env, query)
		if ok && ok2 && cur == prev {
			return
		}
		prev, ok = cur, ok2
	}
}

// eventingConsumeSeconds polls the delivered counter until it has climbed by
// `published` over the baseline, returning the elapsed time. Same conservatism
// as asyncDrainSeconds: only a successful read that confirms the target counts
// as done, so a Prometheus hiccup can't fabricate a ~0s consume.
func eventingConsumeSeconds(ctx context.Context, env *harness.Env, baseline, published float64, timeout time.Duration) (float64, bool) {
	start := time.Now()
	deadline := start.Add(timeout)
	for {
		if v, ok := asyncQueryValue(ctx, env, eventingDeliveredQuery); ok && v-baseline >= published {
			return time.Since(start).Seconds(), true
		}
		if time.Now().After(deadline) {
			return 0, false
		}
		select {
		case <-ctx.Done():
			return 0, false
		case <-time.After(2 * time.Second):
		}
	}
}
