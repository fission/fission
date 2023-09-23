package main

import (
	"fmt"
	"net/http"

	sarama "github.com/IBM/sarama"
)

// Handler posts a message to Kafka Topic
func Handler(w http.ResponseWriter, r *http.Request) {
	brokers := []string{"my-cluster-kafka-brokers.my-kafka-project.svc:9092"}
	producerConfig := sarama.NewConfig()
	producerConfig.Producer.RequiredAcks = sarama.WaitForAll
	producerConfig.Producer.Retry.Max = 100
	producerConfig.Producer.Retry.Backoff = 100
	producerConfig.Producer.Return.Successes = true
	producerConfig.Version = sarama.V1_0_0_0
	producer, err := sarama.NewSyncProducer(brokers, producerConfig)
	fmt.Println("Created a new producer ", producer)
	if err != nil {
		panic(err)
	}
	for i := 0; i < 1000; i++ {
		headers := []sarama.RecordHeader{{Key: []byte("Z-Custom-Name"), Value: []byte("Kafka-Header-test")}}
		_, _, err = producer.SendMessage(&sarama.ProducerMessage{
			Topic:   "topic2",
			Value:   sarama.StringEncoder("{\"name\": \"testvalue\"}"),
			Headers: headers,
		})

		if err != nil {
			w.Write([]byte(fmt.Sprintf("Failed to publish message to topic %s: %v", "testtopic", err)))
			return
		}
	}
	w.Write([]byte("Successfully sent to testtopic"))
}
