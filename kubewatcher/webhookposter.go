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

package kubewatcher

import (
	"io"
	"log"
	"net/http"
	"strings"
)

type (
	Poster struct {
		routerUrl      string
		requestChannel chan *postRequest
	}
	postRequest struct {
		eventType   string
		relativeUrl string
		body        io.Reader
	}
)

func MakePoster(routerUrl string) *Poster {
	p := &Poster{
		routerUrl:      strings.TrimSuffix(routerUrl, "/"),
		requestChannel: make(chan *postRequest, 32), // buffered channel
	}
	go p.svc()
	return p
}

func (p *Poster) svc() {
	for {
		r := <-p.requestChannel

		url := p.routerUrl + r.relativeUrl
		req, err := http.NewRequest("POST", url, r.body)
		if err != nil {
			log.Printf("Failed to create request to %v", r.relativeUrl)
		}
		req.Header.Add("X-Kubernetes-Event-Type", r.eventType)
		req.Header.Add("X-Fission-Request-Async", "true")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("request failed: %v", r)
			// TODO retries, persistence, etc.
		}

		resp.Body.Close()
		if resp.StatusCode != 200 {
			log.Printf("request failed: %v", resp.StatusCode)
			// TODO retries etc.
		}
	}
}

func (p *Poster) Post(eventType, relativeUrl string, body io.Reader) {
	p.requestChannel <- &postRequest{
		eventType:   eventType,
		relativeUrl: relativeUrl,
		body:        body,
	}
}
