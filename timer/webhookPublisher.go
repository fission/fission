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

package timer

import (
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"
)

type (
	// A webhook publisher for a single URL. Satisifies the Publisher interface.
	WebhookPublisher struct {
		requestChannel chan *publishRequest

		maxRetries int
		retryDelay time.Duration

		baseUrl string
	}
	publishRequest struct {
		url        string
		timerName  string
		retries    int
		retryDelay time.Duration
	}
)

func MakeWebhookPublisher(baseUrl string) *WebhookPublisher {
	p := &WebhookPublisher{
		baseUrl:        baseUrl,
		requestChannel: make(chan *publishRequest, 32), // buffered channel
		// TODO make this configurable
		maxRetries: 10,
		retryDelay: 500 * time.Millisecond,
	}
	go p.svc()
	return p
}

func (p *WebhookPublisher) Publish(url string, timerName string) {
	p.requestChannel <- &publishRequest{
		url:        url,
		timerName:  timerName,
		retries:    p.maxRetries,
		retryDelay: p.retryDelay,
	}
}

func (p *WebhookPublisher) svc() {
	for {
		r := <-p.requestChannel
		p.makeHttpRequest(r)
	}
}

func (p *WebhookPublisher) makeHttpRequest(r *publishRequest) {
	url := p.baseUrl + "/" + strings.TrimPrefix(r.url, "/")
	log.Printf("Making HTTP request to %v from timer %v", url, r.timerName)

	// Create request
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Printf("Failed to create request to %v", url)
		// can't do anything more, drop the event.
		return
	}

	req.Header.Add("X-Fission-Timer-Name", r.timerName)

	// Make the request
	resp, err := http.DefaultClient.Do(req)

	// All done if the request succeeded with 200 OK.
	if err == nil && resp.StatusCode == 200 {
		resp.Body.Close()
		return
	}

	// Log errors
	if err != nil {
		log.Printf("Request failed: %v", r)
	} else if resp.StatusCode != 200 {
		log.Printf("Request returned failure: %v", resp.StatusCode)
		body, err := ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if err == nil {
			log.Printf("request error: %v", string(body))
		}
	}

	// Schedule a retry, or give up if out of retries
	r.retries--
	if r.retries > 0 {
		r.retryDelay *= time.Duration(2)
		time.AfterFunc(r.retryDelay, func() {
			p.requestChannel <- r
		})
	} else {
		log.Printf("Final retry failed, giving up on %v", url)
		// Event dropped
	}
}
