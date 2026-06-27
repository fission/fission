// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package kafka

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IBM/sarama"
	"github.com/IBM/sarama/mocks"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestNewKafkaHTTPClient(t *testing.T) {
	t.Run("no auth secret uses an unsigned pooled transport", func(t *testing.T) {
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET", "")
		c := newKafkaHTTPClient()
		require.NotNil(t, c)
		// A dedicated pooled transport (not the shared 2-conn default) sized for
		// per-partition concurrency to the single router-internal host.
		tr, ok := c.Transport.(*http.Transport)
		require.True(t, ok, "expected a *http.Transport")
		assert.NotSame(t, http.DefaultTransport, c.Transport, "must not share the global default transport")
		assert.Equal(t, kafkaIdleConnsPerHost, tr.MaxIdleConnsPerHost)
		// The pool is process-wide, not per trigger: every client shares it, so an
		// mqtrigger pod with many triggers keeps a bounded idle-conn footprint.
		assert.Same(t, kafkaTransport, c.Transport, "all Kafka clients must share one transport")
		assert.Same(t, newKafkaHTTPClient().Transport, c.Transport, "repeated construction must reuse the shared transport")
	})

	t.Run("auth secret wraps the transport with a signer", func(t *testing.T) {
		t.Setenv("FISSION_INTERNAL_AUTH_SECRET", "supersecret")
		c := newKafkaHTTPClient()
		require.NotNil(t, c)
		assert.NotEqual(t, http.DefaultTransport, c.Transport)
		_, isPlainTransport := c.Transport.(*http.Transport)
		assert.False(t, isPlainTransport, "signer should wrap the transport")
	})
}

func nameRefTrigger() *fv1.MessageQueueTrigger {
	tr := &fv1.MessageQueueTrigger{}
	tr.Name = "trig"
	tr.Namespace = "ns1"
	tr.Spec.FunctionReference.Type = fv1.FunctionReferenceTypeFunctionName
	tr.Spec.FunctionReference.Name = "myfn"
	tr.Spec.Topic = "in"
	tr.Spec.ResponseTopic = "resp"
	tr.Spec.ErrorTopic = "err"
	tr.Spec.ContentType = "application/json"
	return tr
}

func TestNewMqtConsumerGroupHandler(t *testing.T) {
	t.Setenv("FISSION_INTERNAL_AUTH_SECRET", "")
	tr := nameRefTrigger()
	ch := NewMqtConsumerGroupHandler(sarama.V2_0_0_0, logr.Discard(), tr, nil, "http://router")

	assert.Contains(t, ch.fnUrl, "myfn")
	assert.Contains(t, ch.fnUrl, "ns1")
	assert.Equal(t, "in", ch.fissionHeaders["X-Fission-MQTrigger-Topic"])
	assert.Equal(t, "resp", ch.fissionHeaders["X-Fission-MQTrigger-RespTopic"])
	assert.Equal(t, "err", ch.fissionHeaders["X-Fission-MQTrigger-ErrorTopic"])
	assert.Equal(t, "application/json", ch.fissionHeaders["Content-Type"])
	require.NotNil(t, ch.httpClient)
}

func newProducerMock(t *testing.T) *mocks.SyncProducer {
	cfg := sarama.NewConfig()
	cfg.Producer.Return.Successes = true
	return mocks.NewSyncProducer(t, cfg)
}

func TestKafkaMsgHandler(t *testing.T) {
	t.Run("200 response is published to the response topic", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Custom", "v")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("function-output"))
		}))
		defer srv.Close()

		producer := newProducerMock(t)
		producer.ExpectSendMessageAndSucceed()
		defer func() { require.NoError(t, producer.Close()) }()

		ch := &MqtConsumerGroupHandler{
			version:        sarama.V2_0_0_0,
			logger:         logr.Discard(),
			trigger:        nameRefTrigger(),
			fissionHeaders: map[string]string{"Content-Type": "application/json"},
			producer:       producer,
			fnUrl:          srv.URL,
			httpClient:     srv.Client(),
		}
		ch.kafkaMsgHandler(&sarama.ConsumerMessage{Value: []byte("hello")})
	})

	t.Run("non-200 response is sent to the error topic", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		producer := newProducerMock(t)
		producer.ExpectSendMessageAndSucceed()
		defer func() { require.NoError(t, producer.Close()) }()

		tr := nameRefTrigger()
		tr.Spec.ResponseTopic = ""
		tr.Spec.MaxRetries = 0
		ch := &MqtConsumerGroupHandler{
			version:    sarama.V2_0_0_0,
			logger:     logr.Discard(),
			trigger:    tr,
			producer:   producer,
			fnUrl:      srv.URL,
			httpClient: srv.Client(),
		}
		ch.kafkaMsgHandler(&sarama.ConsumerMessage{Value: []byte("hello")})
	})

	t.Run("unreachable function exhausts retries and reports to error topic", func(t *testing.T) {
		producer := newProducerMock(t)
		producer.ExpectSendMessageAndSucceed()
		defer func() { require.NoError(t, producer.Close()) }()

		tr := nameRefTrigger()
		tr.Spec.ResponseTopic = ""
		tr.Spec.MaxRetries = 1
		ch := &MqtConsumerGroupHandler{
			version:    sarama.V2_0_0_0,
			logger:     logr.Discard(),
			trigger:    tr,
			producer:   producer,
			fnUrl:      "http://127.0.0.1:1/nope",
			httpClient: http.DefaultClient,
		}
		ch.kafkaMsgHandler(&sarama.ConsumerMessage{Value: []byte("hello")})
	})
}

func TestErrorHandler(t *testing.T) {
	t.Run("publishes to the error topic when set", func(t *testing.T) {
		producer := newProducerMock(t)
		producer.ExpectSendMessageAndSucceed()
		defer func() { require.NoError(t, producer.Close()) }()

		tr := nameRefTrigger()
		errorHandler(logr.Discard(), tr, producer, tr.Spec.ResponseTopic, assert.AnError, nil)
	})

	t.Run("no-ops when no error topic is configured", func(t *testing.T) {
		producer := newProducerMock(t)
		defer func() { require.NoError(t, producer.Close()) }()

		tr := nameRefTrigger()
		tr.Spec.ErrorTopic = ""
		errorHandler(logr.Discard(), tr, producer, "http://fn", assert.AnError, nil)
	})
}
