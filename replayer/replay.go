package replayer

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"

	log "github.com/sirupsen/logrus"

	"github.com/fission/fission/redis/build/gen"
)

// Make return value a proper Response object
func ReplayRequest(routerUrl string, request *redisCache.Request) ([]string, error) {
	path := request.URL["Path"]	// Slash included
	payload := request.URL["Payload"]

	targetUrl := fmt.Sprintf("%v%v", routerUrl, path)

	log.Info("Payload > ", payload)

	// TODO: Make this a header

	var resp *http.Response
	var err error
	client := http.DefaultClient
	if request.Method == http.MethodGet {
		req, err := http.NewRequest("GET", targetUrl, nil)
		if err != nil {
			return []string{}, err
		}
		req.Header.Add("X-Fission-Replayed", "true")
		resp, err = client.Do(req)
	} else {
		req, err := http.NewRequest(request.Method, targetUrl, bytes.NewReader([]byte(payload)))
		if err != nil {
			return []string{}, err
		}
		req.Header.Add("X-Fission-Replayed", "true")
		resp, err = client.Do(req)
	}

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
