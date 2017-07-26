package client

import (
	"bytes"
	"encoding/json"
	"net/http"
	//"time"

	"github.com/fission/fission"
	"github.com/fission/fission/environments/fetcher"
	//"github.com/fission/fission/router"
)

func DoFetchRequest(fetcherUrl string, fr *fetcher.FetchRequest) error {
	body, err := json.Marshal(fr)
	if err != nil {
		return err
	}

	// client := http.Client{
	// 	Transport: router.MakeRetryingRoundTripper(10, 50*time.Millisecond),
	// }

	resp, err := http.Post(fetcherUrl, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fission.MakeErrorFromHTTP(resp)
	}

	return nil
}
