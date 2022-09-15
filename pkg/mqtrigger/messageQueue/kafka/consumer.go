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
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Shopify/sarama"
	"github.com/pkg/errors"
	"go.uber.org/zap"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/mqtrigger"
	"github.com/fission/fission/pkg/utils"
)

type MqtConsumerGroupHandler struct {
	version        sarama.KafkaVersion
	logger         *zap.Logger
	trigger        *fv1.MessageQueueTrigger
	fissionHeaders map[string]string
	producer       sarama.SyncProducer
	fnUrl          string
	ready          chan bool
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
		ready:    make(chan bool),
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

// Setup implemented to satisfy the sarama.ConsumerGroupHandler interface
func (ch MqtConsumerGroupHandler) Setup(session sarama.ConsumerGroupSession) error {
	ch.logger.With(
		zap.String("trigger", ch.trigger.ObjectMeta.Name),
		zap.String("topic", ch.trigger.Spec.Topic),
		zap.String("memberID", session.MemberID()),
		zap.Int32("generationID", session.GenerationID()),
		zap.String("claims", fmt.Sprintf("%v", session.Claims())),
	).Info("consumer group session setup")
	// Mark the consumer as ready
	close(ch.ready)
	return nil
}

// Cleanup implemented to satisfy the sarama.ConsumerGroupHandler interface
func (ch MqtConsumerGroupHandler) Cleanup(session sarama.ConsumerGroupSession) error {
	ch.logger.With(
		zap.String("trigger", ch.trigger.ObjectMeta.Name),
		zap.String("topic", ch.trigger.Spec.Topic),
		zap.String("memberID", session.MemberID()),
		zap.Int32("generationID", session.GenerationID()),
		zap.String("claims", fmt.Sprintf("%v", session.Claims())),
	).Info("consumer group session cleanup")
	return nil
}

// ConsumeClaims implemented to satisfy the sarama.ConsumerGroupHandler interface
func (ch MqtConsumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {

	trigger := ch.trigger.Name
	triggerNamespace := ch.trigger.Namespace
	topic := claim.Topic()
	partition := string(claim.Partition())

	// initially set metrics to -1
	mqtrigger.SetMessageLagCount(trigger, triggerNamespace, topic, partition, -1)

	// Do not move the code below to a goroutine.
	// The `ConsumeClaim` itself is called within a goroutine
	for {
		select {
		case msg := <-claim.Messages():
			if msg != nil {
				ch.kafkaMsgHandler(msg)
				session.MarkMessage(msg, "")
				mqtrigger.IncreaseMessageCount(trigger, triggerNamespace)
			}

			mqtrigger.SetMessageLagCount(trigger, triggerNamespace, topic, partition,
				claim.HighWaterMarkOffset()-msg.Offset-1)

		// Should return when `session.Context()` is done.
		case <-session.Context().Done():
			return nil
		}
	}
}

func (ch *MqtConsumerGroupHandler) kafkaMsgHandler(msg *sarama.ConsumerMessage) {
	value := string(msg.Value)

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
