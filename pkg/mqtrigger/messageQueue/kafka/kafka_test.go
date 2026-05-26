// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package kafka

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fission/fission/pkg/mqtrigger/messageQueue"
)

func TestIsTopicValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		topic string
		want  bool
	}{
		{name: "simple", topic: "orders", want: true},
		{name: "two chars", topic: "ab", want: true},
		{name: "dash dot underscore", topic: "a.b-c_d", want: true},
		{name: "single char (no end class match)", topic: "a", want: false},
		{name: "empty", topic: "", want: false},
		{name: "dot", topic: ".", want: false},
		{name: "double dot", topic: "..", want: false},
		{name: "leading dash", topic: "-abc", want: false},
		{name: "trailing dash", topic: "abc-", want: false},
		{name: "illegal char", topic: "a@b", want: false},
		{name: "too long", topic: strings.Repeat("a", 250), want: false},
		{name: "max length", topic: strings.Repeat("a", 249), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsTopicValid(tt.topic))
		})
	}
}

func TestNew(t *testing.T) {
	logger := logr.Discard()

	t.Run("empty router url", func(t *testing.T) {
		_, err := New(logger, messageQueue.Config{Url: "broker:9092"}, "")
		require.Error(t, err)
	})

	t.Run("empty mq url", func(t *testing.T) {
		_, err := New(logger, messageQueue.Config{Url: ""}, "http://router")
		require.Error(t, err)
	})

	t.Run("splits brokers and defaults to no TLS", func(t *testing.T) {
		t.Setenv("TLS_ENABLED", "false")
		mq, err := New(logger, messageQueue.Config{Url: "b1:9092,b2:9092"}, "http://router")
		require.NoError(t, err)
		k, ok := mq.(Kafka)
		require.True(t, ok)
		assert.Equal(t, []string{"b1:9092", "b2:9092"}, k.brokers)
		assert.False(t, k.tls)
	})

	t.Run("invalid kafka version falls back without error", func(t *testing.T) {
		t.Setenv("MESSAGE_QUEUE_KAFKA_VERSION", "not-a-version")
		_, err := New(logger, messageQueue.Config{Url: "b:9092"}, "http://router")
		require.NoError(t, err)
	})

	t.Run("TLS enabled without secrets errors", func(t *testing.T) {
		t.Setenv("TLS_ENABLED", "true")
		_, err := New(logger, messageQueue.Config{Url: "b:9092", Secrets: nil}, "http://router")
		require.Error(t, err)
	})

	t.Run("TLS enabled with secrets populates authKeys", func(t *testing.T) {
		t.Setenv("TLS_ENABLED", "true")
		mq, err := New(logger, messageQueue.Config{
			Url: "b:9092",
			Secrets: map[string][]byte{
				"caCert":   []byte("ca"),
				"userCert": []byte("cert"),
				"userKey":  []byte("key"),
			},
		}, "http://router")
		require.NoError(t, err)
		k, ok := mq.(Kafka)
		require.True(t, ok)
		assert.True(t, k.tls)
		assert.Equal(t, []byte("cert"), k.authKeys["userCert"])
	})
}

func TestFactoryCreate(t *testing.T) {
	mq, err := (&Factory{}).Create(logr.Discard(), messageQueue.Config{Url: "b:9092"}, "http://router")
	require.NoError(t, err)
	_, ok := mq.(Kafka)
	assert.True(t, ok)
}

// genCertKeyPEM returns a self-signed cert and its private key, both PEM-encoded.
func genCertKeyPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func TestGetTLSConfig(t *testing.T) {
	certPEM, keyPEM := genCertKeyPEM(t)

	t.Run("invalid keypair errors", func(t *testing.T) {
		k := Kafka{logger: logr.Discard(), authKeys: map[string][]byte{
			"userCert": []byte("bogus"),
			"userKey":  []byte("bogus"),
		}}
		_, err := k.getTLSConfig()
		require.Error(t, err)
	})

	t.Run("valid keypair builds config", func(t *testing.T) {
		k := Kafka{logger: logr.Discard(), authKeys: map[string][]byte{
			"userCert": certPEM,
			"userKey":  keyPEM,
			"caCert":   certPEM,
		}}
		cfg, err := k.getTLSConfig()
		require.NoError(t, err)
		assert.Len(t, cfg.Certificates, 1)
		assert.NotNil(t, cfg.RootCAs)
		assert.False(t, cfg.InsecureSkipVerify)
	})

	t.Run("INSECURE_SKIP_VERIFY=true is honoured", func(t *testing.T) {
		t.Setenv("INSECURE_SKIP_VERIFY", "true")
		k := Kafka{logger: logr.Discard(), authKeys: map[string][]byte{
			"userCert": certPEM,
			"userKey":  keyPEM,
			"caCert":   certPEM,
		}}
		cfg, err := k.getTLSConfig()
		require.NoError(t, err)
		assert.True(t, cfg.InsecureSkipVerify)
	})

	t.Run("invalid INSECURE_SKIP_VERIFY errors", func(t *testing.T) {
		t.Setenv("INSECURE_SKIP_VERIFY", "not-a-bool")
		k := Kafka{logger: logr.Discard(), authKeys: map[string][]byte{
			"userCert": certPEM,
			"userKey":  keyPEM,
			"caCert":   certPEM,
		}}
		_, err := k.getTLSConfig()
		require.Error(t, err)
	})
}

// fakeConsumerGroup is a no-op sarama.ConsumerGroup for subscription lifecycle tests.
type fakeConsumerGroup struct{ closed bool }

func (f *fakeConsumerGroup) Consume(context.Context, []string, sarama.ConsumerGroupHandler) error {
	return nil
}
func (f *fakeConsumerGroup) Errors() <-chan error      { return nil }
func (f *fakeConsumerGroup) Close() error              { f.closed = true; return nil }
func (f *fakeConsumerGroup) Pause(map[string][]int32)  {}
func (f *fakeConsumerGroup) Resume(map[string][]int32) {}
func (f *fakeConsumerGroup) PauseAll()                 {}
func (f *fakeConsumerGroup) ResumeAll()                {}

func TestKafkaSubscription(t *testing.T) {
	t.Parallel()
	cg := &fakeConsumerGroup{}
	_, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	sub := &KafkaSubscription{cancel: cancel, consumer: cg, done: done}

	assert.Equal(t, (<-chan struct{})(done), sub.Done())

	require.NoError(t, sub.Stop())
	assert.True(t, cg.closed, "Stop should close the consumer group")

	// Kafka.Unsubscribe delegates to Subscription.Stop.
	cg2 := &fakeConsumerGroup{}
	_, cancel2 := context.WithCancel(t.Context())
	sub2 := &KafkaSubscription{cancel: cancel2, consumer: cg2, done: make(chan struct{})}
	require.NoError(t, Kafka{}.Unsubscribe(sub2))
	assert.True(t, cg2.closed)
}
