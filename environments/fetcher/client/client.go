package client

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"time"

	"github.com/fission/fission"
)

type (
	Client struct {
		url string
	}
)

func MakeClient(fetcherUrl string) *Client {
	return &Client{
		url: fetcherUrl,
	}
}

func (c *Client) getSpecializeUrl() string {
	return c.url + "/specialize"
}

func (c *Client) getFetchUrl() string {
	return c.url + "/fetch"
}

func (c *Client) getUploadUrl() string {
	return c.url + "/upload"
}

func (c *Client) Specialize(req *fission.FunctionSpecializeRequest) error {
	_, err := sendRequest(req, c.getSpecializeUrl())
	return err
}

func (c *Client) Fetch(fr *fission.FunctionFetchRequest) error {
	_, err := sendRequest(fr, c.getFetchUrl())
	return err
}

func (c *Client) Upload(fr *fission.ArchiveUploadRequest) (*fission.ArchiveUploadResponse, error) {
	body, err := sendRequest(fr, c.getUploadUrl())

	uploadResp := fission.ArchiveUploadResponse{}
	err = json.Unmarshal(body, &uploadResp)
	if err != nil {
		return nil, err
	}

	return &uploadResp, nil
}

func sendRequest(req interface{}, url string) ([]byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	maxRetries := 20
	var resp *http.Response

	for i := 0; i < maxRetries; i++ {
		resp, err = http.Post(url, "application/json", bytes.NewReader(body))

		if err == nil {
			if resp.StatusCode == 200 {
				body, err := ioutil.ReadAll(resp.Body)
				if err != nil {
					log.Printf("Error reading response body: %v", err)
				}
				resp.Body.Close()
				return body, err
			}
			err = fission.MakeErrorFromHTTP(resp)
		}

		if i < maxRetries-1 {
			time.Sleep(50 * time.Duration(2*i) * time.Millisecond)
			log.Printf("Error specialize/fetch/upload package (%v), retrying", err)
			continue
		}
	}

	return nil, err
}
