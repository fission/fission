// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package kafka

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"errors"

	"github.com/IBM/sarama"
	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/mqtrigger"
	"github.com/fission/fission/pkg/utils"
	"github.com/fission/fission/pkg/utils/httpx"
)

// newKafkaHTTPClient builds the http.Client used to invoke functions
// through the router. When FISSION_INTERNAL_AUTH_SECRET is set the
// transport is wrapped with hmacauth.ServiceSigner for
// ServiceRouterInternal, so every /fission-function/<ns>/<name>
// request carries the X-Fission-Auth-* headers required by the
// router's internal-listener verifier. Using the per-service derived
// key keeps a leak of this consumer's runtime memory from forging
// requests on other Fission internal channels (storagesvc, fetcher,
// builder, executor). See docs/internal-auth/00-design.md.
// kafkaIdleConnsPerHost sizes the idle connection pool to the single
// router-internal host. ConsumeClaim runs one invocation goroutine per partition,
// so the pool must exceed http.DefaultTransport's 2 idle conns/host; 64 covers
// typical partition counts (the pool only retains what concurrency actually uses).
const kafkaIdleConnsPerHost = 64

// kafkaTransport is the process-wide pooled transport shared by every Kafka
// trigger's HTTP client — ONE connection pool to the single router-internal
// host. Built once (not per trigger) so an mqtrigger pod hosting many triggers
// keeps a bounded idle-conn footprint and reuses connections across triggers;
// the per-trigger HMAC signer wraps it without forking the pool.
var kafkaTransport = httpx.PooledTransport(kafkaIdleConnsPerHost)

func newKafkaHTTPClient() *http.Client {
	var rt http.RoundTripper = kafkaTransport
	if master := os.Getenv("FISSION_INTERNAL_AUTH_SECRET"); master != "" {
		return &http.Client{Transport: hmacauth.ServiceSigner([]byte(master), hmacauth.ServiceRouterInternal, rt, time.Now)}
	}
	return &http.Client{Transport: rt}
}

type MqtConsumerGroupHandler struct {
	version        sarama.KafkaVersion
	logger         logr.Logger
	trigger        *fv1.MessageQueueTrigger
	fissionHeaders map[string]string
	producer       sarama.SyncProducer
	fnUrl          string
	ready          chan bool
	// httpClient invokes the function via the router. It signs each
	// request when FISSION_INTERNAL_AUTH_SECRET is set, so the
	// router's internal-listener HMAC verifier accepts it.
	httpClient *http.Client
}

func NewMqtConsumerGroupHandler(version sarama.KafkaVersion,
	logger logr.Logger,
	trigger *fv1.MessageQueueTrigger,
	producer sarama.SyncProducer,
	routerUrl string) MqtConsumerGroupHandler {
	ch := MqtConsumerGroupHandler{
		version:    version,
		logger:     logger,
		trigger:    trigger,
		producer:   producer,
		ready:      make(chan bool),
		httpClient: newKafkaHTTPClient(),
	}
	// Support other function ref types
	if ch.trigger.Spec.FunctionReference.Type != fv1.FunctionReferenceTypeFunctionName {
		ch.logger.Info("unsupported function reference type for trigger",
			"function_reference_type", ch.trigger.Spec.FunctionReference.Type,
			"trigger", ch.trigger.Name)
		os.Exit(1)
	}
	// Generate the Headers
	ch.fissionHeaders = map[string]string{
		"X-Fission-MQTrigger-Topic":      ch.trigger.Spec.Topic,
		"X-Fission-MQTrigger-RespTopic":  ch.trigger.Spec.ResponseTopic,
		"X-Fission-MQTrigger-ErrorTopic": ch.trigger.Spec.ErrorTopic,
		"Content-Type":                   ch.trigger.Spec.ContentType,
	}
	// RFC-0025: append the alias/version suffix when the reference carries
	// one; resolution stays entirely router-side. Precomputed once here (not
	// per-delivery) since resolution happens per-request on the router side
	// regardless of when the publisher built the URL string.
	ch.fnUrl = routerUrl + "/" + strings.TrimPrefix(utils.UrlForFunctionReference(ch.trigger.Spec.FunctionReference, ch.trigger.Namespace), "/")
	ch.logger.V(1).Info("function HTTP URL", "url", ch.fnUrl)
	return ch
}

// Setup implemented to satisfy the sarama.ConsumerGroupHandler interface
func (ch MqtConsumerGroupHandler) Setup(session sarama.ConsumerGroupSession) error {
	mqtrigger.SetTriggerStatus(ch.trigger.Name, ch.trigger.Namespace)
	mqtrigger.IncreaseInprocessCount()
	ch.logger.WithValues(
		"trigger", ch.trigger.ObjectMeta.Name,
		"topic", ch.trigger.Spec.Topic,
		"memberID", session.MemberID(),
		"generationID", session.GenerationID(),
		"claims", fmt.Sprintf("%v", session.Claims()),
	).Info("consumer group session setup")
	// Mark the consumer as ready
	close(ch.ready)
	return nil
}

// Cleanup implemented to satisfy the sarama.ConsumerGroupHandler interface
func (ch MqtConsumerGroupHandler) Cleanup(session sarama.ConsumerGroupSession) error {
	mqtrigger.ResetTriggerStatus(ch.trigger.Name, ch.trigger.Namespace)
	mqtrigger.DecreaseInprocessCount()
	ch.logger.WithValues(
		"trigger", ch.trigger.ObjectMeta.Name,
		"topic", ch.trigger.Spec.Topic,
		"memberID", session.MemberID(),
		"generationID", session.GenerationID(),
		"claims", fmt.Sprintf("%v", session.Claims()),
	).Info("consumer group session cleanup")
	return nil
}

// ConsumeClaims implemented to satisfy the sarama.ConsumerGroupHandler interface
func (ch MqtConsumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {

	trigger := ch.trigger.Name
	triggerNamespace := ch.trigger.Namespace
	topic := claim.Topic()
	partition := string(claim.Partition())

	// initially set message lag count
	mqtrigger.SetMessageLagCount(trigger, triggerNamespace, topic, partition, claim.HighWaterMarkOffset()-claim.InitialOffset())

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
		ch.logger.Error(err, "failed to create HTTP request to invoke function", "function_url", ch.fnUrl)
		return
	}

	// Set the headers came from Kafka record
	// Using Header.Add() as msg.Headers may have keys with more than one value
	if ch.version.IsAtLeast(sarama.V0_11_0_0) {
		for _, h := range msg.Headers {
			req.Header.Add(string(h.Key), string(h.Value))
		}
	} else {
		ch.logger.Error(nil, "headers are not supported by current Kafka version, needs v0.11+: no record headers to add in HTTP request",
			"current_version", ch.version)
	}

	for k, v := range ch.fissionHeaders {
		req.Header.Set(k, v)
	}

	// Make the request via the per-handler client so HMAC
	// signing (when configured) is applied. Reset the body on every
	// retry: net/http closes req.Body after each Do() call, AND the
	// HMAC signing transport reads it for the canonical hash —
	// without resetting we'd send an empty body on retry and fail
	// signature verification on the receiving side.
	var resp *http.Response
	for attempt := 0; attempt <= ch.trigger.Spec.MaxRetries; attempt++ {
		req.Body = io.NopCloser(strings.NewReader(value))
		// GetBody is also set so net/http's redirect / retryablehttp
		// paths can re-read the body if they need to.
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(value)), nil
		}
		resp, err = ch.httpClient.Do(req)
		if err != nil {
			ch.logger.Error(err, "sending function invocation request failed", "function_url", ch.fnUrl,
				"trigger", ch.trigger.Name)
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
			errors.New(errorString), errorHeaders)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)

	ch.logger.V(1).Info("got response from function invocation",
		"function_url", ch.fnUrl,
		"trigger", ch.trigger.Name,
		"body", string(body))

	if err != nil {
		errorString := "request body error: " + string(body)
		errorHeaders := generateErrorHeaders(errorString)
		errorHandler(ch.logger, ch.trigger, ch.producer, ch.fnUrl,
			fmt.Errorf("%s: %w", errorString, err), errorHeaders)
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
			ch.logger.Error(nil, "headers are not supported by current Kafka version, needs v0.11+: no record headers to add in HTTP request",
				"current_version", ch.version)
		}

		_, _, err := ch.producer.SendMessage(&sarama.ProducerMessage{
			Topic:   ch.trigger.Spec.ResponseTopic,
			Value:   sarama.StringEncoder(body),
			Headers: kafkaRecordHeaders,
		})
		if err != nil {
			ch.logger.Error(err, "failed to publish response body from function invocation to topic", "topic", ch.trigger.Spec.Topic,
				"function_url", ch.fnUrl)
			return
		}
	}
}

func errorHandler(logger logr.Logger, trigger *fv1.MessageQueueTrigger, producer sarama.SyncProducer, funcUrl string, err error, errorTopicHeaders []sarama.RecordHeader) {
	if len(trigger.Spec.ErrorTopic) > 0 {
		_, _, e := producer.SendMessage(&sarama.ProducerMessage{
			Topic:   trigger.Spec.ErrorTopic,
			Value:   sarama.StringEncoder(err.Error()),
			Headers: errorTopicHeaders,
		})
		if e != nil {
			logger.Error(e, "failed to publish message to error topic", "trigger", trigger.Name,
				"message", err.Error(),
				"topic", trigger.Spec.Topic)
		}
	} else {
		logger.Error(nil, "message received to publish to error topic, but no error topic was set", "message", err.Error(), "trigger", trigger.Name, "function_url", funcUrl)
	}
}
