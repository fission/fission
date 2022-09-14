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
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	labels            = []string{"trigger_name", "trigger_namespace"}
	subscriptionCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fission_mqt_subscriptions",
			Help: "Total number of subscriptions to mq currently",
		},
		[]string{},
	)
	messageCount = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_mqt_messages_processed_total",
			Help: "Total number of messages processed",
		},
		labels,
	)
	messageLagCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fission_mqt_message_lag",
			Help: "Total number of messages lag per topic and partition",
		},
		[]string{"trigger_name", "trigger_namespace", "topic", "partition"},
	)
)

func IncreaseSubscriptionCount() {
	subscriptionCount.WithLabelValues().Inc()
}

func DecreaseSubscriptionCount() {
	subscriptionCount.WithLabelValues().Dec()
}

func IncreaseMessageCount(trigname, trignamespace string) {
	messageCount.WithLabelValues(trigname, trignamespace).Inc()
}

func SetMessageLagCount(trigname, trignamespace, topic, partition string, lag int64) {
	messageLagCount.WithLabelValues(trigname, trignamespace, topic, partition).Set(float64(lag))
}
