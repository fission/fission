package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/pkg/errors"
	"github.com/streadway/amqp"
	"go.uber.org/zap"

	"github.com/fission/fission/pkg/mqtrigger/util"
)

type rabbitMQConnData struct {
	queueName       string
	url             string
	includeUnacked  bool
	fissionMetadata util.FissionMetadata
}

func parseRabbitMQConnData() (rabbitMQConnData, error) {
	data := rabbitMQConnData{}
	var err error
	data.fissionMetadata, err = util.ParseFissionMetadata()
	if err != nil {
		return data, err
	}

	if os.Getenv("INCLUDE_UNACKED") != "true" {
		data.includeUnacked = true
	} else {
		data.includeUnacked = false
	}

	data.queueName = os.Getenv("QUEUE_NAME")
	if data.queueName == "" {
		return data, fmt.Errorf("received empty queue name")
	}

	host := ""
	if data.includeUnacked {
		host = os.Getenv("HOST")
	} else {
		data.includeUnacked = true
		host = os.Getenv("API_HOST")
	}
	if host == "" {
		return data, fmt.Errorf("received empty host field")
	}
	data.url = host
	return data, nil
}

func failOnError(logger *zap.Logger, err error, msg string) {
	if err != nil {
		logger.Fatal(msg, zap.Error(err))
	}
}

func consumeMessage(data rabbitMQConnData, logger *zap.Logger, producerChannel, consumerChannel *amqp.Channel) {
	msgs, err := consumerChannel.Consume(
		data.queueName, // queue
		"",             // consumer
		false,          // auto-ack
		false,          // exclusive
		false,          // no-local
		false,          // no-wait
		nil,            // args
	)
	failOnError(logger, err, "failed while reading messages")

	forever := make(chan bool)

	go func() {
		for d := range msgs {
			success := handleFissionFunction(d, data, logger, producerChannel, consumerChannel)
			if success {
				d.Ack(false)
			}
		}
	}()
	logger.Info("Waiting for messages")
	<-forever
}

func handleFissionFunction(d amqp.Delivery, data rabbitMQConnData, logger *zap.Logger, producerChannel, consumerChannel *amqp.Channel) bool {
	var value string = string(d.Body)
	// Generate the Headers
	fissionHeaders := map[string]string{
		"X-Fission-MQTrigger-Topic":      data.fissionMetadata.Topic,
		"X-Fission-MQTrigger-RespTopic":  data.fissionMetadata.ResponseTopic,
		"X-Fission-MQTrigger-ErrorTopic": data.fissionMetadata.ErrorTopic,
		"Content-Type":                   data.fissionMetadata.ContentType,
	}

	// Create request
	req, err := http.NewRequest("POST", data.fissionMetadata.FunctionURL, strings.NewReader(value))
	if err != nil {
		logger.Error("failed to create HTTP request to invoke function",
			zap.Error(err),
			zap.String("function_url", data.fissionMetadata.FunctionURL))
		return false
	}

	for k, v := range fissionHeaders {
		req.Header.Set(k, v)
	}

	// Make the request
	var resp *http.Response
	for attempt := 0; attempt <= data.fissionMetadata.MaxRetries; attempt++ {
		// Make the request
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			logger.Error("sending function invocation request failed",
				zap.Error(err),
				zap.String("function_url", data.fissionMetadata.FunctionURL),
				zap.String("trigger", data.fissionMetadata.TriggerName))
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
			zap.String("function_url", data.fissionMetadata.FunctionURL),
			zap.String("trigger", data.fissionMetadata.TriggerName))
		return false
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)

	logger.Debug("got response from function invocation",
		zap.String("function_url", data.fissionMetadata.FunctionURL),
		zap.String("trigger", data.fissionMetadata.TriggerName),
		zap.String("body", string(body)))

	if err != nil {
		errorHandler(data, logger, producerChannel,
			errors.Wrapf(err, "request body error: %v", string(body)))
		return false
	}
	if resp.StatusCode != 200 {
		errorHandler(data, logger, producerChannel,
			fmt.Errorf("request returned failure: %v", resp.StatusCode))
		return false
	}
	return responseHandler(string(body), data, logger, producerChannel)
}

func errorHandler(data rabbitMQConnData, logger *zap.Logger, producerChannel *amqp.Channel, err error) {
	if len(data.fissionMetadata.ErrorTopic) > 0 {
		err = producerChannel.Publish(
			"",                              // exchange
			data.fissionMetadata.ErrorTopic, // routing key
			false,                           // mandatory
			false,                           // immediate
			amqp.Publishing{
				ContentType: data.fissionMetadata.ContentType,
				Body:        []byte(err.Error()),
			})
		if err != nil {
			logger.Error("failed to publish message to error topic",
				zap.Error(err),
				zap.String("trigger", data.fissionMetadata.TriggerName),
				zap.String("message", err.Error()),
				zap.String("topic", data.fissionMetadata.ErrorTopic))
		}
	} else {
		logger.Error("message received to publish to error topic, but no error topic was set",
			zap.String("message", err.Error()), zap.String("trigger", data.fissionMetadata.TriggerName), zap.String("function_url", data.fissionMetadata.FunctionURL))
	}
}

func responseHandler(response string, data rabbitMQConnData, logger *zap.Logger, producerChannel *amqp.Channel) bool {
	if len(data.fissionMetadata.ResponseTopic) > 0 {
		err := producerChannel.Publish(
			"",                                 // exchange
			data.fissionMetadata.ResponseTopic, // routing key
			false,                              // mandatory
			false,                              // immediate
			amqp.Publishing{
				ContentType: data.fissionMetadata.ContentType,
				Body:        []byte(response),
			})
		if err != nil {
			logger.Warn("failed to publish response body from function invocation to topic",
				zap.Error(err),
				zap.String("topic", data.fissionMetadata.ResponseTopic),
				zap.String("function_url", data.fissionMetadata.FunctionURL))
			return false
		}
	}
	return true
}

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("can't initialize zap logger: %v", err)
	}
	defer logger.Sync()

	data, err := parseRabbitMQConnData()
	failOnError(logger, err, "failed to parse RabbitMQ connection data")

	connection, err := amqp.Dial(data.url)
	failOnError(logger, err, "failed to establish connection with RabbitMQ")
	defer connection.Close()

	producerChannel, err := connection.Channel()
	failOnError(logger, err, "failed to open RabbitMQ channel for producer")
	defer producerChannel.Close()

	consumerChannel, err := connection.Channel()
	failOnError(logger, err, "failed to open RabbitMQ channel for consumer")
	defer consumerChannel.Close()

	consumeMessage(data, logger, producerChannel, consumerChannel)

}
