// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqtrigger

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/fission/fission/pkg/utils/metrics"
)

var (
	// Inc/Dec gauges map to UpDownCounters (exposed by the Prometheus bridge as
	// gauges); the set-to-value lag gauge maps to a synchronous gauge.
	subscriptionCount = metrics.Int64UpDownCounter(
		"fission_mqt_subscriptions",
		"Total number of subscriptions to mq currently",
	)
	messageCount = metrics.Int64Counter(
		"fission_mqt_messages_processed_total",
		"Total number of messages processed",
	)
	messageLagCount = metrics.Int64Gauge(
		"fission_mqt_message_lag",
		"Total number of messages lag per topic and partition",
	)
	triggerStatus = metrics.Int64UpDownCounter(
		"fission_mqt_status",
		"Status of an individual trigger 1 if processing otherwise 0",
	)
	mqtInprocessCount = metrics.Int64UpDownCounter(
		"fission_mqt_inprocess",
		"Total number of MQTs in active processing",
	)
)

func triggerLabels(trigname, trignamespace string) metric.MeasurementOption {
	return metric.WithAttributes(
		attribute.String("trigger_name", trigname),
		attribute.String("trigger_namespace", trignamespace),
	)
}

func IncreaseSubscriptionCount() {
	subscriptionCount.Add(context.Background(), 1)
}

func DecreaseSubscriptionCount() {
	subscriptionCount.Add(context.Background(), -1)
}

func SetTriggerStatus(trigname, trignamespace string) {
	triggerStatus.Add(context.Background(), 1, triggerLabels(trigname, trignamespace))
}

func ResetTriggerStatus(trigname, trignamespace string) {
	triggerStatus.Add(context.Background(), -1, triggerLabels(trigname, trignamespace))
}

func IncreaseInprocessCount() {
	mqtInprocessCount.Add(context.Background(), 1)
}

func DecreaseInprocessCount() {
	mqtInprocessCount.Add(context.Background(), -1)
}

func IncreaseMessageCount(trigname, trignamespace string) {
	messageCount.Add(context.Background(), 1, triggerLabels(trigname, trignamespace))
}

func SetMessageLagCount(trigname, trignamespace, topic, partition string, lag int64) {
	messageLagCount.Record(context.Background(), lag, metric.WithAttributes(
		attribute.String("trigger_name", trigname),
		attribute.String("trigger_namespace", trignamespace),
		attribute.String("topic", topic),
		attribute.String("partition", partition),
	))
}
