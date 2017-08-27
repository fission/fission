package client

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/fission/fission"
)

func DoBuildRequest(builderUrl string, br *fission.PackageBuildRequest) error {
	body, err := json.Marshal(br)
	if err != nil {
		return err
	}

	resp, err := http.Post(builderUrl, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fission.MakeErrorFromHTTP(resp)
	}

	return nil
}
