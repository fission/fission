package client

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"time"

	"github.com/fission/fission"
	"github.com/fission/fission/environments/fetcher"
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

func (c *Client) Fetch(fr *fetcher.FetchRequest) error {
	body, err := json.Marshal(fr)
	if err != nil {
		return err
	}

	maxRetries := 20
	var resp *http.Response

	for i := 0; i < maxRetries; i++ {
		resp, err = http.Post(c.url, "application/json", bytes.NewReader(body))

		if err == nil {
			if resp.StatusCode == 200 {
				resp.Body.Close()
				return nil
			}
			err = fission.MakeErrorFromHTTP(resp)
		}

		if i < maxRetries-1 {
			time.Sleep(50 * time.Duration(2*i) * time.Millisecond)
			log.Printf("Error fetching package (%v), retrying", err)
			continue
		}

		log.Printf("Failed to fetch: %v", err)
		return err
	}

	return nil
}

func (c *Client) Upload(fr *fetcher.UploadRequest) (*fetcher.UploadResponse, error) {
	body, err := json.Marshal(fr)
	if err != nil {
		return nil, err
	}
	resp, err := http.Post(c.url+"/upload", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fission.MakeErrorFromHTTP(resp)
	}

	rBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	uploadResp := fetcher.UploadResponse{}
	err = json.Unmarshal([]byte(rBody), &uploadResp)
	if err != nil {
		return nil, err
	}

	return &uploadResp, nil
}
