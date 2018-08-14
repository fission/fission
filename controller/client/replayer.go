package client

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// TODO: Move to separate file?
func (c *Client) ReplayByReqUID(reqUID string) ([]string, error) {
	relativeUrl := fmt.Sprintf("replay/%v", reqUID)

	resp, err := http.Get(c.url(relativeUrl))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	fmt.Println("Received: ", resp.Status)

	body, err := c.handleResponse(resp)		// Might be some problems here
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