// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package mqtrigger

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/fission/fission/pkg/utils/metrics"
)

var (
	subscriptionCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fission_mqt_subscriptions",
			Help: "Total number of subscriptions to mq currently",
		},
		[]string{},
	)
	messageCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_mqt_messages_processed_total",
			Help: "Total number of messages processed",
		},
		[]string{"trigger_name", "trigger_namespace"},
	)
	messageLagCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fission_mqt_message_lag",
			Help: "Total number of messages lag per topic and partition",
		},
		[]string{"trigger_name", "trigger_namespace", "topic", "partition"},
	)
	triggerStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fission_mqt_status",
			Help: "Status of an individual trigger 1 if processing otherwise 0",
		},
		[]string{"trigger_name", "trigger_namespace"},
	)
	mqtInprocessCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fission_mqt_inprocess",
			Help: "Total number of MQTs in active processing",
		},
		[]string{},
	)
)

func IncreaseSubscriptionCount() {
	subscriptionCount.WithLabelValues().Inc()
}

func DecreaseSubscriptionCount() {
	subscriptionCount.WithLabelValues().Dec()
}

func SetTriggerStatus(trigname, trignamespace string) {
	triggerStatus.WithLabelValues(trigname, trignamespace).Inc()
}

func ResetTriggerStatus(trigname, trignamespace string) {
	triggerStatus.WithLabelValues(trigname, trignamespace).Dec()
}

func IncreaseInprocessCount() {
	mqtInprocessCount.WithLabelValues().Inc()
}

func DecreaseInprocessCount() {
	mqtInprocessCount.WithLabelValues().Dec()
}

func IncreaseMessageCount(trigname, trignamespace string) {
	messageCount.WithLabelValues(trigname, trignamespace).Inc()
}

func SetMessageLagCount(trigname, trignamespace, topic, partition string, lag int64) {
	messageLagCount.WithLabelValues(trigname, trignamespace, topic, partition).Set(float64(lag))
}

func init() {
	registry := metrics.Registry
	registry.MustRegister(subscriptionCount)
	registry.MustRegister(messageCount)
	registry.MustRegister(messageLagCount)
	registry.MustRegister(mqtInprocessCount)
	registry.MustRegister(triggerStatus)
}
