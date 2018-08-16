/*
Copyright 2018 The Fission Authors.

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
package client

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func (c *Client) ReplayByReqUID(reqUID string) ([]string, error) {
	relativeUrl := fmt.Sprintf("replay/%v", reqUID)

	resp, err := http.Get(c.url(relativeUrl))
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	fmt.Println("Received: ", resp.Status)

	body, err := c.handleResponse(resp)
	if err != nil {
		return nil, err
	}

	replayed := make([]string, 0)

	err = json.Unmarshal(body, &replayed)
	if err != nil {
		return nil, err
	}

	return replayed, nil
}
