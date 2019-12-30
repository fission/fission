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
		logger      *zap.Logger
		routerUrl   string
		rabbitMQURI string
		//rabbitMQVersion string
	}
)

func makeRabbitMQMessageQueue(logger *zap.Logger, routerUrl string, mqCfg MessageQueueConfig) (MessageQueue, error) {
	if len(routerUrl) == 0 || len(mqCfg.Url) == 0 {
		return nil, errors.New("The royter url or the MQ url is empty.")
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

	msgs, err := ch.Consume(
		queue.Name,
		"",
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
	return sub.(*amqp.Channel).Close()
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
	if err != nil {
		rabbitMQ.logger.Error("error getting body from the function call response",
			zap.String("errror", err.Error()))
	}

	rabbitMQ.logger.Info("got response from function invocation",
		zap.String("function_url", url),
		zap.String("trigger", trigger.Metadata.Name),
		zap.String("body", string(body)))

}
