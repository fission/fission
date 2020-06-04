package main

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"hash"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Shopify/sarama"
	"github.com/xdg/scram"
	"go.uber.org/zap"
)

type kafkaMetadata struct {
	bootstrapServers []string
	group            string
	topic            string
	lagThreshold     int64

	// auth
	authMode kafkaAuthMode
	username string
	password string

	// ssl
	cert string
	key  string
	ca   string

	// fission
	responseTopic string
	errorTopic    string
	groupUID      string
	functionURL   string
}

type kafkaAuthMode string

const (
	kafkaAuthModeForNone            kafkaAuthMode = "none"
	kafkaAuthModeForSaslPlaintext   kafkaAuthMode = "sasl_plaintext"
	kafkaAuthModeForSaslScramSha256 kafkaAuthMode = "sasl_scram_sha256"
	kafkaAuthModeForSaslScramSha512 kafkaAuthMode = "sasl_scram_sha512"
	kafkaAuthModeForSaslSSL         kafkaAuthMode = "sasl_ssl"
	kafkaAuthModeForSaslSSLPlain    kafkaAuthMode = "sasl_ssl_plain"
)

var SHA256 scram.HashGeneratorFcn = func() hash.Hash { return sha256.New() }
var SHA512 scram.HashGeneratorFcn = func() hash.Hash { return sha512.New() }

type XDGSCRAMClient struct {
	*scram.Client
	*scram.ClientConversation
	scram.HashGeneratorFcn
}

var producer sarama.SyncProducer

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
		return meta, errors.New("no consumer group given")
	}
	meta.group = os.Getenv("CONSUMER_GROUP")

	if os.Getenv("TOPIC") == "" {
		return meta, errors.New("no topic given")
	}
	meta.topic = os.Getenv("TOPIC")

	if os.Getenv("LAG_THRESHOLD") == "" {
		return meta, errors.New("no lagThreshold given")
	}
	val, err := strconv.ParseInt(os.Getenv("LAG_THRESHOLD"), 10, 64)
	if err != nil {
		return meta, fmt.Errorf("error parsing lagThreshold: %s", err)
	}
	meta.lagThreshold = val

	meta.authMode = kafkaAuthModeForNone
	mode := kafkaAuthMode(strings.TrimSpace((os.Getenv("AUTH_MODE"))))

	if mode != kafkaAuthModeForNone && mode != kafkaAuthModeForSaslPlaintext && mode != kafkaAuthModeForSaslSSL && mode != kafkaAuthModeForSaslSSLPlain && mode != kafkaAuthModeForSaslScramSha256 && mode != kafkaAuthModeForSaslScramSha512 {
		return meta, fmt.Errorf("err auth mode %s given", mode)
	}

	meta.authMode = mode

	if meta.authMode != kafkaAuthModeForNone && meta.authMode != kafkaAuthModeForSaslSSL {
		if os.Getenv("USERNAME") == "" {
			return meta, errors.New("no username given")
		}
		meta.username = strings.TrimSpace(os.Getenv("USERNAME"))

		if os.Getenv("PASSWORD") == "" {
			return meta, errors.New("no password given")
		}
		meta.password = strings.TrimSpace(os.Getenv("PASSWORD"))
	}

	if meta.authMode == kafkaAuthModeForSaslSSL {
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
	if os.Getenv("GROUP_UID") == "" {
		return meta, errors.New("No Group UID given")
	}
	meta.groupUID = os.Getenv("GROUP_UID")

	if os.Getenv("ERROR_TOPIC") == "" {
		return meta, errors.New("No Error Topic given")
	}
	meta.errorTopic = os.Getenv("ERROR_TOPIC")

	return meta, nil
}

func getConfig(metadata kafkaMetadata) (*sarama.Config, error) {
	config := sarama.NewConfig()
	config.Version = sarama.V1_0_0_0

	if ok := metadata.authMode == kafkaAuthModeForSaslPlaintext || metadata.authMode == kafkaAuthModeForSaslSSLPlain || metadata.authMode == kafkaAuthModeForSaslScramSha256 || metadata.authMode == kafkaAuthModeForSaslScramSha512; ok {
		config.Net.SASL.Enable = true
		config.Net.SASL.User = metadata.username
		config.Net.SASL.Password = metadata.password
	}

	if metadata.authMode == kafkaAuthModeForSaslSSLPlain {
		config.Net.SASL.Mechanism = sarama.SASLMechanism(sarama.SASLTypePlaintext)

		tlsConfig := &tls.Config{
			InsecureSkipVerify: true,
			ClientAuth:         0,
		}

		config.Net.TLS.Enable = true
		config.Net.TLS.Config = tlsConfig
		config.Net.DialTimeout = 10 * time.Second
	}

	if metadata.authMode == kafkaAuthModeForSaslSSL {
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

	if metadata.authMode == kafkaAuthModeForSaslScramSha256 {
		config.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient { return &XDGSCRAMClient{HashGeneratorFcn: SHA256} }
		config.Net.SASL.Mechanism = sarama.SASLMechanism(sarama.SASLTypeSCRAMSHA256)
	}

	if metadata.authMode == kafkaAuthModeForSaslScramSha512 {
		config.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient { return &XDGSCRAMClient{HashGeneratorFcn: SHA512} }
		config.Net.SASL.Mechanism = sarama.SASLMechanism(sarama.SASLTypeSCRAMSHA512)
	}

	if metadata.authMode == kafkaAuthModeForSaslPlaintext {
		config.Net.SASL.Mechanism = sarama.SASLTypePlaintext
		config.Net.TLS.Enable = true
	}
	return config, nil
}

// Consumer represents a Sarama consumer group consumer
type Consumer struct {
	ready chan bool
}

// Setup is run at the beginning of a new session, before ConsumeClaim
func (consumer *Consumer) Setup(sarama.ConsumerGroupSession) error {
	close(consumer.ready)
	return nil
}

// Cleanup is run at the end of a session, once all ConsumeClaim goroutines have exited
func (consumer *Consumer) Cleanup(sarama.ConsumerGroupSession) error {
	return nil
}

// ConsumeClaim must start a consumer loop of ConsumerGroupClaim's Messages()
func (consumer *Consumer) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for message := range claim.Messages() {
		log.Printf("Message claimed: value = %s, timestamp = %v, topic = %s", string(message.Value), message.Timestamp, message.Topic)
		session.MarkMessage(message, "")
	}
	return nil
}

func initProducer(metadata kafkaMetadata) error {
	config, err := getConfig(metadata)
	if err != nil {
		return err
	}

	config.Producer.RequiredAcks = sarama.WaitForAll
	config.Producer.Retry.Max = 10
	config.Producer.Return.Successes = true
	producer, err = sarama.NewSyncProducer(metadata.bootstrapServers, config)
	if err != nil {
		return err
	}
	return nil
}

func handleFissionFunction(msg *sarama.ConsumerMessage) {
	// To Do
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
	config, err := getConfig(metadata)
	if err != nil {
		logger.Error("Failed to create kafka config", zap.Error(err))
		return
	}

	err = initProducer(metadata)
	if err != nil {
		logger.Error("Failed to create kafka producer", zap.Error(err))
		return
	}
	defer producer.Close()

	consumer := Consumer{
		ready: make(chan bool),
	}

	ctx, cancel := context.WithCancel(context.Background())
	client, err := sarama.NewConsumerGroup(metadata.bootstrapServers, metadata.groupUID, config)
	if err != nil {
		logger.Error("Error creating consumer group client", zap.Error(err))
		return
	}

	wg := &sync.WaitGroup{}
	wg.Add(1)

	go func() {
		defer wg.Done()
		for {
			if err := client.Consume(ctx, []string{metadata.topic}, &consumer); err != nil {
				logger.Error("Error from consumer", zap.Error(err))
			}
			// check if context was cancelled, signaling that the consumer should stop
			if ctx.Err() != nil {
				return
			}
			consumer.ready = make(chan bool)
		}
	}()

	<-consumer.ready // Await till the consumer has been set up
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
