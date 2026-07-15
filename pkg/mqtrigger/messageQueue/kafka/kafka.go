// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package kafka

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/IBM/sarama"
	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/mqtrigger/egress"
	"github.com/fission/fission/pkg/mqtrigger/factory"
	"github.com/fission/fission/pkg/mqtrigger/messageQueue"
	"github.com/fission/fission/pkg/mqtrigger/mqpub"
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

// consumerConfig builds the sarama consumer config (TLS included when enabled).
func (kafka Kafka) consumerConfig() (*sarama.Config, error) {
	cfg := sarama.NewConfig()
	cfg.Consumer.Return.Errors = true
	cfg.Version = kafka.version
	if kafka.tls {
		tlsConfig, err := kafka.getTLSConfig()
		if err != nil {
			return nil, err
		}
		cfg.Net.TLS.Enable = true
		cfg.Net.TLS.Config = tlsConfig
	}
	return cfg, nil
}

// producerConfig builds the sarama producer config — shared by the
// per-subscription response/error-topic producer and the RFC-0027 egress
// publisher, so broker/TLS settings cannot drift between the two paths.
func (kafka Kafka) producerConfig() (*sarama.Config, error) {
	cfg := sarama.NewConfig()
	cfg.Producer.RequiredAcks = sarama.WaitForAll
	cfg.Producer.Retry.Max = 10
	cfg.Producer.Return.Successes = true
	cfg.Version = kafka.version
	if kafka.tls {
		tlsConfig, err := kafka.getTLSConfig()
		if err != nil {
			return nil, err
		}
		cfg.Net.TLS.Enable = true
		cfg.Net.TLS.Config = tlsConfig
	}
	return cfg, nil
}

// NewEgressPublisher implements egress.BrokerPublisherProvider: it opens one
// shared SyncProducer and returns the publish function the egress consumer
// loop executes jobs with. RequiredAcks=WaitForAll means a nil error is a
// broker ack (E4 — the settle that follows is honest).
func (kafka Kafka) NewEgressPublisher() (egress.PublishFunc, error) {
	cfg, err := kafka.producerConfig()
	if err != nil {
		return nil, err
	}
	producer, err := sarama.NewSyncProducer(kafka.brokers, cfg)
	if err != nil {
		return nil, fmt.Errorf("kafka egress: creating producer: %w", err)
	}
	return func(_ context.Context, job mqpub.EgressJob) error {
		// Kafka topics are flat: the fission topic name is used as-is; the
		// source namespace travels as a header for provenance.
		_, _, err := producer.SendMessage(&sarama.ProducerMessage{
			Topic: job.Topic,
			Value: sarama.ByteEncoder(job.Payload),
			Headers: []sarama.RecordHeader{
				{Key: []byte("content-type"), Value: []byte(job.ContentType)},
				{Key: []byte("fission-namespace"), Value: []byte(job.Namespace)},
			},
		})
		return err
	}, nil
}

func (kafka Kafka) Subscribe(ctx context.Context, trigger *fv1.MessageQueueTrigger) (messageQueue.Subscription, error) {
	kafka.logger.V(1).Info("inside kakfa subscribe", "trigger", trigger)
	kafka.logger.V(1).Info("brokers set", "brokers", kafka.brokers)

	consumerConfig, err := kafka.consumerConfig()
	if err != nil {
		return nil, err
	}
	producerConfig, err := kafka.producerConfig()
	if err != nil {
		return nil, err
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

	if v := os.Getenv("INSECURE_SKIP_VERIFY"); v != "" {
		skipVerify, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("invalid INSECURE_SKIP_VERIFY=%q: %w", v, err)
		}
		if skipVerify {
			kafka.logger.Info("WARNING: TLS certificate verification disabled for Kafka (INSECURE_SKIP_VERIFY=true); use only for self-signed dev clusters")
		}
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
