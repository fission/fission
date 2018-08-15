package replayer

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/fission/fission/redis/build/gen"
)

func ReplayRequest(routerUrl string, request *redisCache.Request) ([]string, error) {
	path := request.URL["Path"] // Includes slash prefix
	payload := request.URL["Payload"]

	targetUrl := fmt.Sprintf("%v%v", routerUrl, path)

	var req *http.Request
	var err error
	client := http.DefaultClient

	if request.Method == http.MethodGet {
		req, err = http.NewRequest("GET", targetUrl, nil)
		if err != nil {
			return []string{}, err
		}
	} else {
		req, err = http.NewRequest(request.Method, targetUrl, bytes.NewReader([]byte(payload)))
		if err != nil {
			return []string{}, err
		}
	}
	
	req.Header.Add("X-Fission-Replayed", "true")
	resp, err := client.Do(req)

	if err != nil {
		return []string{}, errors.New(fmt.Sprintf("failed to make request: %v", err))
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return []string{}, errors.New(fmt.Sprintf("failed to read response: %v", err))
	}

	bodyStr := string(body)

	return []string{bodyStr}, nil
}
