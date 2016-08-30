package router

import (
	"net/http"
	"log"
	"io/ioutil"
)

func testRequest(targetUrl string, expectedResponse string) {
	resp, err := http.Get(targetUrl)
	if (err != nil) {
		log.Panic("failed make get request")
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if (err != nil) {
		log.Panic("failed to read response")
	}

	bodyStr := string(body)
	log.Printf("Server responded with %v", bodyStr)
	if (bodyStr != expectedResponse) {
		log.Panic("Unexpected response")
	}	
}

