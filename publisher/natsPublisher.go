/*
Copyright 2017 The Fission Authors.

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

package publisher

import (
	"log"
	"time"

	ns "github.com/nats-io/go-nats-streaming"
)

type (
	// A webhook publisher for a single URL. Satisifies the Publisher interface.
	NatsPublisher struct {
		requestChannel chan *natsRequest

		maxRetries int
		retryDelay time.Duration

		nsConn *ns.Conn
	}
	natsRequest struct {
		body       string
		target     string
		retries    int
		retryDelay time.Duration
	}
)

const (
	natsClientID = "fissionPublisher"
)

func MakeNatsPublisher(conn *ns.Conn) *NatsPublisher {
	p := &NatsPublisher{
		nsConn:         conn,
		requestChannel: make(chan *natsRequest, 32), // buffered channel
		// TODO make this configurable
		maxRetries: 10,
		retryDelay: 500 * time.Millisecond,
	}
	go p.svc()
	return p
}

func (p *NatsPublisher) Publish(body string, headers map[string]string, target string) {
	p.requestChannel <- &natsRequest{
		body:       body,
		target:     target,
		retries:    p.maxRetries,
		retryDelay: p.retryDelay,
	}
}

func (p *NatsPublisher) svc() {
	for {
		r := <-p.requestChannel
		p.makeNatsRequest(r)
	}
}

func (p *NatsPublisher) makeNatsRequest(r *natsRequest) {
	err := (*p.nsConn).Publish(r.target, []byte(r.body))
	if err == nil {
		return
	}
	// Schedule a retry, or give up if out of retries
	r.retries--
	if r.retries > 0 {
		r.retryDelay *= time.Duration(2)
		time.AfterFunc(r.retryDelay, func() {
			p.requestChannel <- r
		})
	} else {
		log.Printf("Error: %v", err)
		log.Printf("Final retry failed, giving up on %v", r.target)
		// Event dropped
	}
}
