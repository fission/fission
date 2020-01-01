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

package messageQueue

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"

	fv1 "github.com/fission/fission/pkg/apis/fission.io/v1"
	"github.com/fission/fission/pkg/types"
	"github.com/fission/fission/pkg/utils"
	"github.com/pkg/errors"
	"github.com/streadway/amqp"
	"go.uber.org/zap"
)

type (
	RabbitMQ struct {
		logger        *zap.Logger
		routerUrl     string
		rabbitMQURI   string
		serverChannel *amqp.Channel
		//rabbitMQVersion string
	}
)

func makeRabbitMQMessageQueue(logger *zap.Logger, routerUrl string, mqCfg MessageQueueConfig) (MessageQueue, error) {
	if len(routerUrl) == 0 || len(mqCfg.Url) == 0 {
		return nil, errors.New("The router url or the MQ url is empty.")
	}

	//rabbitMQVersion := os.Getenv("MESSAGE_QUEUE_RABBITMQ_VERSION")

	rabbitMQ := RabbitMQ{
		logger:      logger,
		routerUrl:   routerUrl,
		rabbitMQURI: mqCfg.Url,
	}

	logger.Info("Created RabbitMQ ", zap.Any("RabbitMQ URI ", mqCfg.Url))
	return rabbitMQ, nil
}

func (rabbitMQ RabbitMQ) subscribe(trigger *fv1.MessageQueueTrigger) (messageQueueSubscription, error) {
	rabbitMQ.logger.Info("inside rabbitmq subscribe", zap.Any("trigger", trigger))
	conn, err := amqp.Dial(rabbitMQ.rabbitMQURI)

	if err != nil {
		return nil, err
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return nil, err
	}
	rabbitMQ.serverChannel = ch

	queue, err := ch.QueueDeclare(
		trigger.Spec.Topic,
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return nil, err
	}

	// 	The consumer is identified by a string that is unique and scoped for all
	// consumers on this channel.  If you wish to eventually cancel the consumer, use
	// the same non-empty identifier in Channel.Cancel
	msgs, err := ch.Consume(
		queue.Name,
		"func-cons",
		true,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return nil, err
	}

	forever := make(chan bool)

	go func() {
		for msg := range msgs {
			go rabbitMQMessageHandler(&rabbitMQ, trigger, msg)
		}
	}()

	<-forever
	return conn, nil
}

func (rabbitMQ RabbitMQ) unsubscribe(sub messageQueueSubscription) error {
	return rabbitMQ.serverChannel.Cancel("func-cons", true)
}

func rabbitMQMessageHandler(rabbitMQ *RabbitMQ, trigger *fv1.MessageQueueTrigger, msg amqp.Delivery) {

	if trigger.Spec.FunctionReference.Type != types.FunctionReferenceTypeFunctionName {
		rabbitMQ.logger.Fatal("unsupported function reference type for trigger",
			zap.Any("function_reference_type", trigger.Spec.FunctionReference.Type),
			zap.String("trigger", trigger.Metadata.Name))
	}

	url := rabbitMQ.routerUrl + "/" + strings.TrimPrefix(utils.UrlForFunction(trigger.Spec.FunctionReference.Name, trigger.Metadata.Namespace), "/")
	rabbitMQ.logger.Info("making HTTP request", zap.String("url", url))

	// Generate the Headers
	fissionHeaders := map[string]string{
		"X-Fission-MQTrigger-Topic":      trigger.Spec.Topic,
		"X-Fission-MQTrigger-RespTopic":  trigger.Spec.ResponseTopic,
		"X-Fission-MQTrigger-ErrorTopic": trigger.Spec.ErrorTopic,
		"Content-Type":                   trigger.Spec.ContentType,
	}

	// Create request
	req, err := http.NewRequest("POST", url, strings.NewReader(string(msg.Body)))
	if err != nil {
		rabbitMQ.logger.Error("failed to create HTTP request to invoke function",
			zap.Error(err),
			zap.String("function_url", url))
		return
	}

	for k, v := range fissionHeaders {
		req.Header.Set(k, v)
	}

	// Make the request
	var resp *http.Response
	for attempt := 0; attempt <= trigger.Spec.MaxRetries; attempt++ {
		// Make the request
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			rabbitMQ.logger.Error("sending function invocation request failed",
				zap.Error(err),
				zap.String("function_url", url),
				zap.String("trigger", trigger.Metadata.Name))
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
		rabbitMQ.logger.Warn("every function invocation retry failed; final retry gave empty response",
			zap.String("function_url", url),
			zap.String("trigger", trigger.Metadata.Name))
		return
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)

	rabbitMQ.logger.Info("got response from function invocation",
		zap.String("function_url", url),
		zap.String("trigger", trigger.Metadata.Name),
		zap.String("body", string(body)))

	if err != nil {
		rabbitMQErrorHandler(rabbitMQ.logger, trigger, rabbitMQ.serverChannel, url,
			errors.Wrapf(err, "request body error %v", string(body)))
		return
	}

	if resp.StatusCode != 200 {
		rabbitMQErrorHandler(rabbitMQ.logger, trigger, rabbitMQ.serverChannel, url,
			fmt.Errorf("request returned failure %v", resp.StatusCode))
		return
	}

	if len(trigger.Spec.ResponseTopic) > 0 {
		// produce the response to the output topic
		err = rabbitMQ.serverChannel.Publish(
			"",
			trigger.Spec.ResponseTopic,
			false,
			false,
			amqp.Publishing{
				ContentType: "text/plain",
				Body:        body,
			},
		)

		if err != nil {
			rabbitMQ.logger.Warn("failed to publish response body to output topic",
				zap.Error(err),
				zap.String("topic", trigger.Spec.ResponseTopic),
				zap.String("function_url", url),
			)
			return
		}
	}

}

func rabbitMQErrorHandler(logger *zap.Logger, trigger *fv1.MessageQueueTrigger, serverChannel *amqp.Channel, url string,
	errMsg error) {
	if len(trigger.Spec.ErrorTopic) > 0 {
		err := serverChannel.Publish(
			"",
			trigger.Spec.ErrorTopic,
			false,
			false,
			amqp.Publishing{
				ContentType: "text/plain",
				Body:        []byte(errMsg.Error()),
			},
		)

		if err != nil {
			logger.Error("failed to publish message to error topic",
				zap.Error(err),
				zap.String("trigger", trigger.Metadata.Name),
				zap.String("message", err.Error()),
				zap.String("topic", trigger.Spec.ErrorTopic),
			)
		}
	} else {
		logger.Error("message received to publish to error topic, but no error topic was set",
			zap.String("message", errMsg.Error()),
			zap.String("trigger", trigger.Metadata.Name),
			zap.String("function_url", url),
		)
	}
}
