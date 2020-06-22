package main

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"hash"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Shopify/sarama"
	"github.com/pkg/errors"
	"github.com/xdg/scram"
	"go.uber.org/zap"

	mqt "github.com/fission/fission/pkg/mqtrigger"
)

type kafkaMetadata struct {
	bootstrapServers []string
	consumerGroup    string

	// auth
	authMode kafkaAuthMode
	username string
	password string

	// ssl
	cert string
	key  string
	ca   string
}

type kafkaAuthMode string

const (
	kafkaAuthModeNone            kafkaAuthMode = "none"
	kafkaAuthModeSaslPlaintext   kafkaAuthMode = "sasl_plaintext"
	kafkaAuthModeSaslScramSha256 kafkaAuthMode = "sasl_scram_sha256"
	kafkaAuthModeSaslScramSha512 kafkaAuthMode = "sasl_scram_sha512"
	kafkaAuthModeSaslSSL         kafkaAuthMode = "sasl_ssl"
	kafkaAuthModeSaslSSLPlain    kafkaAuthMode = "sasl_ssl_plain"
)

var SHA256 scram.HashGeneratorFcn = func() hash.Hash { return sha256.New() }
var SHA512 scram.HashGeneratorFcn = func() hash.Hash { return sha512.New() }

type XDGSCRAMClient struct {
	*scram.Client
	*scram.ClientConversation
	scram.HashGeneratorFcn
}

func (x *XDGSCRAMClient) Begin(userName, password, authzID string) (err error) {
	x.Client, err = x.HashGeneratorFcn.NewClient(userName, password, authzID)
	if err != nil {
		return err
	}
	x.ClientConversation = x.Client.NewConversation()
	return nil
}

func (x *XDGSCRAMClient) Step(challenge string) (response string, err error) {
	response, err = x.ClientConversation.Step(challenge)
	return
}

func (x *XDGSCRAMClient) Done() bool {
	return x.ClientConversation.Done()
}

func parseKafkaMetadata(logger *zap.Logger) (kafkaMetadata, error) {
	meta := kafkaMetadata{}

	// brokerList marked as deprecated, bootstrapServers is the new one to use
	if os.Getenv("BROKER_LIST") != "" && os.Getenv("BOOTSTRAP_SERVERS") != "" {
		return meta, errors.New("cannot specify both bootstrapServers and brokerList (deprecated)")
	}
	if os.Getenv("BROKER_LIST") == "" && os.Getenv("BOOTSTRAP_SERVERS") == "" {
		return meta, errors.New("no bootstrapServers or brokerList (deprecated) given")
	}
	if os.Getenv("BOOTSTRAP_SERVERS") != "" {
		meta.bootstrapServers = strings.Split(os.Getenv("BOOTSTRAP_SERVERS"), ",")
	}
	if os.Getenv("BROKER_LIST") != "" {
		logger.Info("WARNING: usage of brokerList is deprecated. use bootstrapServers instead.")
		meta.bootstrapServers = strings.Split(os.Getenv("BROKER_LIST"), ",")
	}
	if os.Getenv("CONSUMER_GROUP") == "" {
		return meta, errors.New("No consumerGroup given")
	}
	meta.consumerGroup = os.Getenv("CONSUMER_GROUP")

	meta.authMode = kafkaAuthModeNone
	mode := kafkaAuthMode(strings.TrimSpace((os.Getenv("AUTH_MODE"))))
	if mode == "" {
		mode = kafkaAuthModeNone
	}

	if mode != kafkaAuthModeNone && mode != kafkaAuthModeSaslPlaintext && mode != kafkaAuthModeSaslSSL && mode != kafkaAuthModeSaslSSLPlain && mode != kafkaAuthModeSaslScramSha256 && mode != kafkaAuthModeSaslScramSha512 {
		return meta, fmt.Errorf("err auth mode %s given", mode)
	}

	meta.authMode = mode

	if meta.authMode != kafkaAuthModeNone && meta.authMode != kafkaAuthModeSaslSSL {
		if os.Getenv("USERNAME") == "" {
			return meta, errors.New("no username given")
		}
		meta.username = strings.TrimSpace(os.Getenv("USERNAME"))

		if os.Getenv("PASSWORD") == "" {
			return meta, errors.New("no password given")
		}
		meta.password = strings.TrimSpace(os.Getenv("PASSWORD"))
	}

	if meta.authMode == kafkaAuthModeSaslSSL {
		if os.Getenv("CA") == "" {
			return meta, errors.New("no ca given")
		}
		meta.ca = os.Getenv("CA")

		if os.Getenv("CERT") == "" {
			return meta, errors.New("no cert given")
		}
		meta.cert = os.Getenv("CERT")

		if os.Getenv("KEY") == "" {
			return meta, errors.New("no key given")
		}
		meta.key = os.Getenv("KEY")
	}

	return meta, nil
}

func getConfig(metadata kafkaMetadata) (*sarama.Config, error) {
	config := sarama.NewConfig()
	config.Version = sarama.V1_0_0_0

	if ok := metadata.authMode == kafkaAuthModeSaslPlaintext || metadata.authMode == kafkaAuthModeSaslSSLPlain || metadata.authMode == kafkaAuthModeSaslScramSha256 || metadata.authMode == kafkaAuthModeSaslScramSha512; ok {
		config.Net.SASL.Enable = true
		config.Net.SASL.User = metadata.username
		config.Net.SASL.Password = metadata.password
	}

	if metadata.authMode == kafkaAuthModeSaslSSLPlain {
		config.Net.SASL.Mechanism = sarama.SASLMechanism(sarama.SASLTypePlaintext)

		tlsConfig := &tls.Config{
			InsecureSkipVerify: true,
			ClientAuth:         0,
		}

		config.Net.TLS.Enable = true
		config.Net.TLS.Config = tlsConfig
		config.Net.DialTimeout = 10 * time.Second
	}

	if metadata.authMode == kafkaAuthModeSaslSSL {
		cert, err := tls.X509KeyPair([]byte(metadata.cert), []byte(metadata.key))
		if err != nil {
			return nil, fmt.Errorf("error parse X509KeyPair: %s", err)
		}

		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM([]byte(metadata.ca))

		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{cert},
			RootCAs:      caCertPool,
		}

		config.Net.TLS.Enable = true
		config.Net.TLS.Config = tlsConfig
	}

	if metadata.authMode == kafkaAuthModeSaslScramSha256 {
		config.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient { return &XDGSCRAMClient{HashGeneratorFcn: SHA256} }
		config.Net.SASL.Mechanism = sarama.SASLMechanism(sarama.SASLTypeSCRAMSHA256)
	}

	if metadata.authMode == kafkaAuthModeSaslScramSha512 {
		config.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient { return &XDGSCRAMClient{HashGeneratorFcn: SHA512} }
		config.Net.SASL.Mechanism = sarama.SASLMechanism(sarama.SASLTypeSCRAMSHA512)
	}

	if metadata.authMode == kafkaAuthModeSaslPlaintext {
		config.Net.SASL.Mechanism = sarama.SASLTypePlaintext
		config.Net.TLS.Enable = true
	}
	return config, nil
}

// Connector represents a Sarama consumer group consumer
type Connector struct {
	ready                chan bool
	logger               *zap.Logger
	producer             sarama.SyncProducer
	fissionTriggerFields mqt.FissionMetadata
}

// Setup is run at the beginning of a new session, before ConsumeClaim
func (connector *Connector) Setup(sarama.ConsumerGroupSession) error {
	close(connector.ready)
	return nil
}

// Cleanup is run at the end of a session, once all ConsumeClaim goroutines have exited
func (connector *Connector) Cleanup(sarama.ConsumerGroupSession) error {
	return nil
}

// ConsumeClaim must start a consumer loop of ConsumerGroupClaim's Messages()
func (connector *Connector) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for message := range claim.Messages() {
		connector.logger.Info(fmt.Sprintf("Message claimed: value = %s, timestamp = %v, topic = %s", string(message.Value), message.Timestamp, message.Topic))
		success := handleFissionFunction(message, connector.fissionTriggerFields, connector.producer, connector.logger)
		if success {
			session.MarkMessage(message, "")
		}
	}
	return nil
}

func getProducer(metadata kafkaMetadata) (sarama.SyncProducer, error) {
	config, err := getConfig(metadata)
	if err != nil {
		return nil, err
	}

	config.Producer.RequiredAcks = sarama.WaitForAll
	config.Producer.Retry.Max = 10
	config.Producer.Return.Successes = true
	producer, err := sarama.NewSyncProducer(metadata.bootstrapServers, config)
	if err != nil {
		return nil, err
	}
	return producer, nil
}

func handleFissionFunction(msg *sarama.ConsumerMessage, triggerFields mqt.FissionMetadata, producer sarama.SyncProducer, logger *zap.Logger) bool {
	var value string = string(msg.Value[:])
	// Generate the Headers
	fissionHeaders := map[string]string{
		"X-Fission-MQTrigger-Topic":      triggerFields.Topic,
		"X-Fission-MQTrigger-RespTopic":  triggerFields.ResponseTopic,
		"X-Fission-MQTrigger-ErrorTopic": triggerFields.ErrorTopic,
		"Content-Type":                   triggerFields.ContentType,
	}

	// Create request
	req, err := http.NewRequest("POST", triggerFields.FunctionURL, strings.NewReader(value))
	if err != nil {
		logger.Error("failed to create HTTP request to invoke function",
			zap.Error(err),
			zap.String("function_url", triggerFields.FunctionURL))
		return false
	}

	// Set the headers came from Kafka record
	// Using Header.Add() as msg.Headers may have keys with more than one value
	for _, h := range msg.Headers {
		req.Header.Add(string(h.Key), string(h.Value))
	}

	for k, v := range fissionHeaders {
		req.Header.Set(k, v)
	}

	// Make the request
	var resp *http.Response
	for attempt := 0; attempt <= triggerFields.MaxRetries; attempt++ {
		// Make the request
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			logger.Error("sending function invocation request failed",
				zap.Error(err),
				zap.String("function_url", triggerFields.FunctionURL),
				zap.String("trigger", triggerFields.TriggerName))
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

	if resp == nil {
		logger.Warn("every function invocation retry failed; final retry gave empty response",
			zap.String("function_url", triggerFields.FunctionURL),
			zap.String("trigger", triggerFields.TriggerName))
		return false
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)

	logger.Debug("got response from function invocation",
		zap.String("function_url", triggerFields.FunctionURL),
		zap.String("trigger", triggerFields.TriggerName),
		zap.String("body", string(body)))

	if err != nil {
		errorHandler(logger, triggerFields, producer,
			errors.Wrapf(err, "request body error: %v", string(body)))
		return false
	}
	if resp.StatusCode != 200 {
		errorHandler(logger, triggerFields, producer,
			fmt.Errorf("request returned failure: %v", resp.StatusCode))
		return false
	}

	// Generate Kafka record headers
	var kafkaRecordHeaders []sarama.RecordHeader

	for k, v := range resp.Header {
		// One key may have multiple values
		for _, v := range v {
			kafkaRecordHeaders = append(kafkaRecordHeaders, sarama.RecordHeader{Key: []byte(k), Value: []byte(v)})
		}
	}

	_, _, err = producer.SendMessage(&sarama.ProducerMessage{
		Topic:   triggerFields.ResponseTopic,
		Value:   sarama.StringEncoder(body),
		Headers: kafkaRecordHeaders,
	})
	if err != nil {
		logger.Warn("failed to publish response body from function invocation to topic",
			zap.Error(err),
			zap.String("topic", triggerFields.Topic),
			zap.String("function_url", triggerFields.FunctionURL))
		return false
	}

	return true
}

func errorHandler(logger *zap.Logger, triggerFields mqt.FissionMetadata, producer sarama.SyncProducer, err error) {
	if len(triggerFields.ErrorTopic) > 0 {
		_, _, e := producer.SendMessage(&sarama.ProducerMessage{
			Topic: triggerFields.ErrorTopic,
			Value: sarama.StringEncoder(err.Error()),
		})
		if e != nil {
			logger.Error("failed to publish message to error topic",
				zap.Error(e),
				zap.String("trigger", triggerFields.TriggerName),
				zap.String("message", err.Error()),
				zap.String("topic", triggerFields.Topic))
		}
	} else {
		logger.Error("message received to publish to error topic, but no error topic was set",
			zap.String("message", err.Error()), zap.String("trigger", triggerFields.TriggerName), zap.String("function_url", triggerFields.FunctionURL))
	}
}

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer logger.Sync()

	metadata, err := parseKafkaMetadata(logger)
	if err != nil {
		logger.Error("Failed to fetch kafka metadata", zap.Error(err))
		return
	}

	triggerFields, err := mqt.ParseFissionMetadata()
	if err != nil {
		logger.Error("Failed to parse fission trigger fields", zap.Error(err))
		return
	}

	config, err := getConfig(metadata)
	if err != nil {
		logger.Error("Failed to create kafka config", zap.Error(err))
		return
	}

	producer, err := getProducer(metadata)
	if err != nil {
		logger.Error("Failed to create kafka producer", zap.Error(err))
		return
	}
	defer producer.Close()

	connector := Connector{
		ready:                make(chan bool),
		logger:               logger,
		producer:             producer,
		fissionTriggerFields: triggerFields,
	}

	ctx, cancel := context.WithCancel(context.Background())
	client, err := sarama.NewConsumerGroup(metadata.bootstrapServers, metadata.consumerGroup, config)
	if err != nil {
		logger.Error("Error creating consumer group client", zap.Error(err))
		return
	}

	wg := &sync.WaitGroup{}
	wg.Add(1)

	go func() {
		defer wg.Done()
		for {
			if err := client.Consume(ctx, []string{triggerFields.Topic}, &connector); err != nil {
				logger.Error("Error from consumer", zap.Error(err))
			}
			// check if context was cancelled, signaling that the consumer should stop
			if ctx.Err() != nil {
				return
			}
			connector.ready = make(chan bool)
		}
	}()

	<-connector.ready // Await till the consumer has been set up
	logger.Info("Sarama consumer up and running!...")
	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-ctx.Done():
		logger.Info("terminating: context cancelled")
	case <-sigterm:
		logger.Info("terminating: via signal")
	}
	cancel()
	wg.Wait()
	if err = client.Close(); err != nil {
		logger.Error("Error closing client", zap.Error(err))
	}
}
