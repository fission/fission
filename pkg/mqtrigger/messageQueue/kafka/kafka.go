/*
Copyright 2016 The Fission Authors.

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

package kafka

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"regexp"
	"strconv"
	"strings"

	"errors"

	"github.com/IBM/sarama"
	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/mqtrigger/factory"
	"github.com/fission/fission/pkg/mqtrigger/messageQueue"
	"github.com/fission/fission/pkg/mqtrigger/validator"
)

func init() {
	factory.Register(fv1.MessageQueueTypeKafka, &Factory{})
	validator.Register(fv1.MessageQueueTypeKafka, IsTopicValid)
}

var (
	// Need to use raw string to support escape sequence for - & . chars
	validKafkaTopicName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9\-\._]*[a-zA-Z0-9]$`)

	// Map for ErrorTopic messages to maintain recycle counter
	errorMessageMap = make(map[string]int)
)

type (
	Kafka struct {
		logger    logr.Logger
		routerUrl string
		brokers   []string
		version   sarama.KafkaVersion
		authKeys  map[string][]byte
		tls       bool
	}

	Factory struct{}
)

// KafkaSubscription implements messageQueue.Subscription for Kafka consumers.
type KafkaSubscription struct {
	ctx      context.Context
	cancel   context.CancelFunc
	consumer sarama.ConsumerGroup
	done     chan struct{}
}

// Stop gracefully stops the Kafka subscription.
func (s *KafkaSubscription) Stop() error {
	s.cancel()
	return s.consumer.Close()
}

// Done returns a channel that is closed when the subscription is stopped.
func (s *KafkaSubscription) Done() <-chan struct{} {
	return s.done
}

func (factory *Factory) Create(logger logr.Logger, mqCfg messageQueue.Config, routerUrl string) (messageQueue.MessageQueue, error) {
	return New(logger, mqCfg, routerUrl)
}

func New(logger logr.Logger, mqCfg messageQueue.Config, routerUrl string) (messageQueue.MessageQueue, error) {
	if len(routerUrl) == 0 || len(mqCfg.Url) == 0 {
		return nil, errors.New("the router URL or MQ URL is empty")
	}
	mqKafkaVersion := os.Getenv("MESSAGE_QUEUE_KAFKA_VERSION")

	// Parse version string
	kafkaVersion, err := sarama.ParseKafkaVersion(mqKafkaVersion)
	if err != nil {
		logger.Error(err, "error parsing kafka version string - falling back to default", "failed_version", mqKafkaVersion,
			"default_version", kafkaVersion)
	}

	kafka := Kafka{
		logger:    logger.WithName("kafka"),
		routerUrl: routerUrl,
		brokers:   strings.Split(mqCfg.Url, ","),
		version:   kafkaVersion,
	}

	if tls, _ := strconv.ParseBool(os.Getenv("TLS_ENABLED")); tls {
		kafka.tls = true

		authKeys := make(map[string][]byte)

		if mqCfg.Secrets == nil {
			return nil, errors.New("no secrets were loaded")
		}

		authKeys["caCert"] = mqCfg.Secrets["caCert"]
		authKeys["userCert"] = mqCfg.Secrets["userCert"]
		authKeys["userKey"] = mqCfg.Secrets["userKey"]
		kafka.authKeys = authKeys
	}

	logger.Info("created kafka queue", "kafka brokers", kafka.brokers,
		"kafka version", kafka.version)
	return kafka, nil
}

func (kafka Kafka) Subscribe(ctx context.Context, trigger *fv1.MessageQueueTrigger) (messageQueue.Subscription, error) {
	kafka.logger.V(1).Info("inside kakfa subscribe", "trigger", trigger)
	kafka.logger.V(1).Info("brokers set", "brokers", kafka.brokers)

	// Create new consumer
	consumerConfig := sarama.NewConfig()
	consumerConfig.Consumer.Return.Errors = true
	consumerConfig.Version = kafka.version

	// Create new producer
	producerConfig := sarama.NewConfig()
	producerConfig.Producer.RequiredAcks = sarama.WaitForAll
	producerConfig.Producer.Retry.Max = 10
	producerConfig.Producer.Return.Successes = true
	producerConfig.Version = kafka.version

	// Setup TLS for both producer and consumer
	if kafka.tls {
		tlsConfig, err := kafka.getTLSConfig()

		if err != nil {
			return nil, err
		}

		producerConfig.Net.TLS.Enable = true
		producerConfig.Net.TLS.Config = tlsConfig
		consumerConfig.Net.TLS.Enable = true
		consumerConfig.Net.TLS.Config = tlsConfig
	}

	consumer, err := sarama.NewConsumerGroup(kafka.brokers, string(trigger.UID), consumerConfig)
	if err != nil {
		return nil, err
	}

	producer, err := sarama.NewSyncProducer(kafka.brokers, producerConfig)
	if err != nil {
		return nil, err
	}

	kafka.logger.Info("created a new producer and a new consumer", "brokers", kafka.brokers,
		"topic", trigger.Spec.Topic,
		"response topic", trigger.Spec.ResponseTopic,
		"error topic", trigger.Spec.ErrorTopic,
		"trigger", trigger.Name,
		"function namespace", trigger.Namespace,
		"function name", trigger.Spec.FunctionReference.Name)

	// consume errors
	go func() {
		for err := range consumer.Errors() {
			kafka.logger.WithValues("trigger", trigger.ObjectMeta.Name, "topic", trigger.Spec.Topic).Error(err, "consumer error received")
		}
	}()

	// Create a cancellable context that respects both parent context and our cancel function
	subCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	ch := NewMqtConsumerGroupHandler(kafka.version, kafka.logger, trigger, producer, kafka.routerUrl)

	// consume messages
	go func() {
		defer close(done)
		topic := []string{trigger.Spec.Topic}
		// Create a new session for the consumer group until the context is cancelled
		for {
			// Consume messages
			err := consumer.Consume(subCtx, topic, ch)
			if err != nil {
				kafka.logger.Error(err, "consumer error", "trigger", trigger.Name)
			}

			if subCtx.Err() != nil {
				kafka.logger.Info("consumer context cancelled", "trigger", trigger.Name)
				return
			}
			ch.ready = make(chan bool)
		}
	}()

	<-ch.ready // wait for consumer to be ready

	subscription := &KafkaSubscription{
		ctx:      subCtx,
		cancel:   cancel,
		consumer: consumer,
		done:     done,
	}
	return subscription, nil
}

func (kafka Kafka) getTLSConfig() (*tls.Config, error) {
	tlsConfig := tls.Config{}
	cert, err := tls.X509KeyPair(kafka.authKeys["userCert"], kafka.authKeys["userKey"])
	if err != nil {
		return nil, err
	}

	tlsConfig.Certificates = []tls.Certificate{cert}

	skipVerify, err := strconv.ParseBool(os.Getenv("INSECURE_SKIP_VERIFY"))
	if err != nil {
		kafka.logger.Error(nil, "failed to parse value of env variable INSECURE_SKIP_VERIFY taking default value false, expected boolean value: true/false", "received", os.Getenv("INSECURE_SKIP_VERIFY"))
	} else {
		tlsConfig.InsecureSkipVerify = skipVerify
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(kafka.authKeys["caCert"])
	tlsConfig.RootCAs = caCertPool

	return &tlsConfig, nil
}

func (kafka Kafka) Unsubscribe(subscription messageQueue.Subscription) error {
	return subscription.Stop()
}

// The validation is based on Kafka's internal implementation:
// https://github.com/apache/kafka/blob/cde6d18983b5d58199f8857d8d61d7efcbe6e54a/clients/src/main/java/org/apache/kafka/common/internals/Topic.java#L36-L47
func IsTopicValid(topic string) bool {
	if len(topic) == 0 {
		return false
	}
	if topic == "." || topic == ".." {
		return false
	}
	if len(topic) > 249 {
		return false
	}
	if !validKafkaTopicName.MatchString(topic) {
		return false
	}
	return true
}
