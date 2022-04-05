package mqtrigger

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	metricsAddr       = ":8080"
	labels            = []string{"trigger_name", "trigger_namespace"}
	subscriptionCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "fission_mqt_subscriptions_total",
			Help: "Total number of subscriptions to mq currently",
		},
		labels,
	)
	messageCount = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fission_mqt_messages_processed_total",
			Help: "Total number of messages processed",
		},
		labels,
	)
)

func IncreaseSubscriptionCount(trigname, trignamespace string) {
	subscriptionCount.WithLabelValues(trigname, trignamespace).Inc()
}

func DecreaseSubscriptionCount(trigname, trignamespace string) {
	subscriptionCount.WithLabelValues(trigname, trignamespace).Dec()
}

func IncreaseMessageCount(trigname, trignamespace string) {
	subscriptionCount.WithLabelValues(trigname, trignamespace).Inc()
}
