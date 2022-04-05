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
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/Shopify/sarama"
	"github.com/pkg/errors"
	"go.uber.org/zap"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/mqtrigger"
	"github.com/fission/fission/pkg/mqtrigger/factory"
	"github.com/fission/fission/pkg/mqtrigger/messageQueue"
	"github.com/fission/fission/pkg/mqtrigger/validator"
	"github.com/fission/fission/pkg/utils"
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
		logger    *zap.Logger
		routerUrl string
		brokers   []string
		version   sarama.KafkaVersion
		authKeys  map[string][]byte
		tls       bool
	}

	Factory struct{}
)

type MqtConsumerGroupHandler struct {
	version        sarama.KafkaVersion
	logger         *zap.Logger
	trigger        *fv1.MessageQueueTrigger
	fissionHeaders map[string]string
	producer       sarama.SyncProducer
	fnUrl          string
}

type MqtConsumer struct {
	ctx      context.Context
	cancel   context.CancelFunc
	consumer sarama.ConsumerGroup
}

func NewMqtConsumerGroupHandler(version sarama.KafkaVersion,
	logger *zap.Logger,
	trigger *fv1.MessageQueueTrigger,
	producer sarama.SyncProducer,
	routerUrl string) MqtConsumerGroupHandler {
	ch := MqtConsumerGroupHandler{
		version:  version,
		logger:   logger,
		trigger:  trigger,
		producer: producer,
	}
	// Support other function ref types
	if ch.trigger.Spec.FunctionReference.Type != fv1.FunctionReferenceTypeFunctionName {
		ch.logger.Fatal("unsupported function reference type for trigger",
			zap.Any("function_reference_type", ch.trigger.Spec.FunctionReference.Type),
			zap.String("trigger", ch.trigger.ObjectMeta.Name))
	}
	// Generate the Headers
	ch.fissionHeaders = map[string]string{
		"X-Fission-MQTrigger-Topic":      ch.trigger.Spec.Topic,
		"X-Fission-MQTrigger-RespTopic":  ch.trigger.Spec.ResponseTopic,
		"X-Fission-MQTrigger-ErrorTopic": ch.trigger.Spec.ErrorTopic,
		"Content-Type":                   ch.trigger.Spec.ContentType,
	}
	ch.fnUrl = routerUrl + "/" + strings.TrimPrefix(utils.UrlForFunction(ch.trigger.Spec.FunctionReference.Name, ch.trigger.ObjectMeta.Namespace), "/")
	ch.logger.Debug("function HTTP URL", zap.String("url", ch.fnUrl))
	return ch
}

func (ch MqtConsumerGroupHandler) Setup(sarama.ConsumerGroupSession) error {
	return nil
}

func (ch MqtConsumerGroupHandler) Cleanup(sarama.ConsumerGroupSession) error {
	return nil
}

func (ch MqtConsumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for msg := range claim.Messages() {
		ch.kafkaMsgHandler(session, msg)
		mqtrigger.IncreaseMessageCount(ch.trigger.Name, ch.trigger.Namespace)
	}
	return nil
}

//func (ch *MqtConsumerGroupHandler) kafkaMsgHandler(kafka *Kafka, producer sarama.SyncProducer, trigger *fv1.MessageQueueTrigger, msg *sarama.ConsumerMessage, consumer *cluster.Consumer) {

func (ch *MqtConsumerGroupHandler) kafkaMsgHandler(session sarama.ConsumerGroupSession, msg *sarama.ConsumerMessage) {
	var value string = string(msg.Value[:])

	// Create request
	req, err := http.NewRequest("POST", ch.fnUrl, strings.NewReader(value))
	if err != nil {
		ch.logger.Error("failed to create HTTP request to invoke function",
			zap.Error(err),
			zap.String("function_url", ch.fnUrl))
		return
	}

	// Set the headers came from Kafka record
	// Using Header.Add() as msg.Headers may have keys with more than one value
	if ch.version.IsAtLeast(sarama.V0_11_0_0) {
		for _, h := range msg.Headers {
			req.Header.Add(string(h.Key), string(h.Value))
		}
	} else {
		ch.logger.Warn("headers are not supported by current Kafka version, needs v0.11+: no record headers to add in HTTP request",
			zap.Any("current_version", ch.version))
	}

	for k, v := range ch.fissionHeaders {
		req.Header.Set(k, v)
	}

	// Make the request
	var resp *http.Response
	for attempt := 0; attempt <= ch.trigger.Spec.MaxRetries; attempt++ {
		// Make the request
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			ch.logger.Error("sending function invocation request failed",
				zap.Error(err),
				zap.String("function_url", ch.fnUrl),
				zap.String("trigger", ch.trigger.ObjectMeta.Name))
			continue
		}
		if resp == nil {
			continue
		}
		if err == nil && resp.StatusCode == http.StatusOK {
			// Success, quit retrying
			break
		}
	}

	generateErrorHeaders := func(errString string) []sarama.RecordHeader {
		var errorHeaders []sarama.RecordHeader
		if ch.version.IsAtLeast(sarama.V0_11_0_0) {
			if count, ok := errorMessageMap[errString]; ok {
				errorMessageMap[errString] = count + 1
			} else {
				errorMessageMap[errString] = 1
			}
			errorHeaders = append(errorHeaders, sarama.RecordHeader{Key: []byte("MessageSource"), Value: []byte(ch.trigger.Spec.Topic)})
			errorHeaders = append(errorHeaders, sarama.RecordHeader{Key: []byte("RecycleCounter"), Value: []byte(strconv.Itoa(errorMessageMap[errString]))})
		}
		return errorHeaders
	}

	if resp == nil {
		errorString := fmt.Sprintf("request exceed retries: %v", ch.trigger.Spec.MaxRetries)
		errorHeaders := generateErrorHeaders(errorString)
		errorHandler(ch.logger, ch.trigger, ch.producer, ch.fnUrl,
			fmt.Errorf(errorString), errorHeaders)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)

	ch.logger.Debug("got response from function invocation",
		zap.String("function_url", ch.fnUrl),
		zap.String("trigger", ch.trigger.ObjectMeta.Name),
		zap.String("body", string(body)))

	if err != nil {
		errorString := "request body error: " + string(body)
		errorHeaders := generateErrorHeaders(errorString)
		errorHandler(ch.logger, ch.trigger, ch.producer, ch.fnUrl,
			errors.Wrapf(err, errorString), errorHeaders)
		return
	}
	if resp.StatusCode != 200 {
		errorString := fmt.Sprintf("request returned failure: %v, request body error: %v", resp.StatusCode, body)
		errorHeaders := generateErrorHeaders(errorString)
		errorHandler(ch.logger, ch.trigger, ch.producer, ch.fnUrl,
			fmt.Errorf("request returned failure: %v", resp.StatusCode), errorHeaders)
		return
	}
	if len(ch.trigger.Spec.ResponseTopic) > 0 {
		// Generate Kafka record headers
		var kafkaRecordHeaders []sarama.RecordHeader
		if ch.version.IsAtLeast(sarama.V0_11_0_0) {
			for k, v := range resp.Header {
				// One key may have multiple values
				for _, v := range v {
					kafkaRecordHeaders = append(kafkaRecordHeaders, sarama.RecordHeader{Key: []byte(k), Value: []byte(v)})
				}
			}
		} else {
			ch.logger.Warn("headers are not supported by current Kafka version, needs v0.11+: no record headers to add in HTTP request",
				zap.Any("current_version", ch.version))
		}

		_, _, err := ch.producer.SendMessage(&sarama.ProducerMessage{
			Topic:   ch.trigger.Spec.ResponseTopic,
			Value:   sarama.StringEncoder(body),
			Headers: kafkaRecordHeaders,
		})
		if err != nil {
			ch.logger.Warn("failed to publish response body from function invocation to topic",
				zap.Error(err),
				zap.String("topic", ch.trigger.Spec.Topic),
				zap.String("function_url", ch.fnUrl))
			return
		}
	}
	session.MarkMessage(msg, "")
}

func (factory *Factory) Create(logger *zap.Logger, mqCfg messageQueue.Config, routerUrl string) (messageQueue.MessageQueue, error) {
	return New(logger, mqCfg, routerUrl)
}

func New(logger *zap.Logger, mqCfg messageQueue.Config, routerUrl string) (messageQueue.MessageQueue, error) {
	if len(routerUrl) == 0 || len(mqCfg.Url) == 0 {
		return nil, errors.New("the router URL or MQ URL is empty")
	}
	mqKafkaVersion := os.Getenv("MESSAGE_QUEUE_KAFKA_VERSION")

	// Parse version string
	kafkaVersion, err := sarama.ParseKafkaVersion(mqKafkaVersion)
	if err != nil {
		logger.Warn("error parsing kafka version string - falling back to default",
			zap.Error(err),
			zap.String("failed_version", mqKafkaVersion),
			zap.Any("default_version", kafkaVersion))
	}

	kafka := Kafka{
		logger:    logger.Named("kafka"),
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

	logger.Info("created kafka queue", zap.Any("kafka brokers", kafka.brokers),
		zap.Any("kafka version", kafka.version))
	return kafka, nil
}

func (kafka Kafka) Subscribe(trigger *fv1.MessageQueueTrigger) (messageQueue.Subscription, error) {
	kafka.logger.Info("inside kakfa subscribe", zap.Any("trigger", trigger))
	kafka.logger.Info("brokers set", zap.Strings("brokers", kafka.brokers))

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
		consumerConfig.Net.TLS.Enable = true
		producerConfig.Net.TLS.Enable = true
		tlsConfig, err := kafka.getTLSConfig()

		if err != nil {
			return nil, err
		}

		producerConfig.Net.TLS.Config = tlsConfig
		consumerConfig.Net.TLS.Config = tlsConfig
	}

	consumer, err := sarama.NewConsumerGroup(kafka.brokers, string(trigger.ObjectMeta.UID), consumerConfig)
	// consumer, err := cluster.NewConsumer(kafka.brokers, string(trigger.ObjectMeta.UID), []string{trigger.Spec.Topic}, consumerConfig)
	kafka.logger.Info("created a new consumer", zap.Strings("brokers", kafka.brokers),
		zap.String("input topic", trigger.Spec.Topic),
		zap.String("output topic", trigger.Spec.ResponseTopic),
		zap.String("error topic", trigger.Spec.ErrorTopic),
		zap.String("trigger name", trigger.ObjectMeta.Name),
		zap.String("function namespace", trigger.ObjectMeta.Namespace),
		zap.String("function name", trigger.Spec.FunctionReference.Name))
	if err != nil {
		return nil, err
	}

	producer, err := sarama.NewSyncProducer(kafka.brokers, producerConfig)
	kafka.logger.Info("created a new producer", zap.Strings("brokers", kafka.brokers),
		zap.String("input topic", trigger.Spec.Topic),
		zap.String("output topic", trigger.Spec.ResponseTopic),
		zap.String("error topic", trigger.Spec.ErrorTopic),
		zap.String("trigger name", trigger.ObjectMeta.Name),
		zap.String("function namespace", trigger.ObjectMeta.Namespace),
		zap.String("function name", trigger.Spec.FunctionReference.Name))

	if err != nil {
		return nil, err
	}

	// consume errors
	go func() {
		for err := range consumer.Errors() {
			kafka.logger.Error("consumer error", zap.Error(err))
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	ch := NewMqtConsumerGroupHandler(kafka.version, kafka.logger, trigger, producer, kafka.routerUrl)

	// consume messages
	go func() {
		topic := []string{trigger.Spec.Topic}
		err = consumer.Consume(ctx, topic, ch)
		if err != nil {
			kafka.logger.Error("consumer error", zap.Error(err))
		}

		if ctx.Err() != nil {
			return
		}
	}()

	mqtConsumer := MqtConsumer{
		ctx:      ctx,
		cancel:   cancel,
		consumer: consumer,
	}
	return mqtConsumer, nil
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
		kafka.logger.Error("failed to parse value of env variable INSECURE_SKIP_VERIFY taking default value false, expected boolean value: true/false",
			zap.String("received", os.Getenv("INSECURE_SKIP_VERIFY")))
	} else {
		tlsConfig.InsecureSkipVerify = skipVerify
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(kafka.authKeys["caCert"])
	tlsConfig.RootCAs = caCertPool

	return &tlsConfig, nil
}

func (kafka Kafka) Unsubscribe(subscription messageQueue.Subscription) error {
	mqtConsumer := subscription.(MqtConsumer)
	mqtConsumer.cancel()
	return mqtConsumer.consumer.Close()
}

func errorHandler(logger *zap.Logger, trigger *fv1.MessageQueueTrigger, producer sarama.SyncProducer, funcUrl string, err error, errorTopicHeaders []sarama.RecordHeader) {
	if len(trigger.Spec.ErrorTopic) > 0 {
		_, _, e := producer.SendMessage(&sarama.ProducerMessage{
			Topic:   trigger.Spec.ErrorTopic,
			Value:   sarama.StringEncoder(err.Error()),
			Headers: errorTopicHeaders,
		})
		if e != nil {
			logger.Error("failed to publish message to error topic",
				zap.Error(e),
				zap.String("trigger", trigger.ObjectMeta.Name),
				zap.String("message", err.Error()),
				zap.String("topic", trigger.Spec.Topic))
		}
	} else {
		logger.Error("message received to publish to error topic, but no error topic was set",
			zap.String("message", err.Error()), zap.String("trigger", trigger.ObjectMeta.Name), zap.String("function_url", funcUrl))
	}
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
