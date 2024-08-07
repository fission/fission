/*
Copyright 2022 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mqtrigger

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/fission/fission/pkg/utils/metrics"
)

var (
	labels            = []string{"trigger_name", "trigger_namespace"}
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
		labels,
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
