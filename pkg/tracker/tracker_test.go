package tracker

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	uuid "github.com/satori/go.uuid"
	"github.com/stretchr/testify/assert"
)

func TestSendEvent(t *testing.T) {
	id, err := uuid.NewV4()
	if err != nil {
		log.Panic(err)
	}
	tracker := &tracker{
		cid: id.String(),
	}

	event := &Event{
		Label: "play",
		Value: "value",
	}

	//Testcase 1
	t.Run("GA Tracking ID should not be empty", func(test *testing.T) {
		resp := tracker.SendEvent(context.Background(), *event)
		assert.NotNil(test, resp, "error can't be nil")
		assert.Equal(test, resp.Error(), "tracker.SendEvent: GA_TRACKING_ID env not set",
			"SendEvent must return an error in case gaPropertyID is empty")
	})

	tracker.gaPropertyID = "UA-000000-2"
	//Testcase 2
	t.Run("category and action should not be empty", func(test *testing.T) {
		resp := tracker.SendEvent(context.Background(), *event)
		assert.NotNil(test, resp, "error can't be nil")
		assert.Equal(test, resp.Error(), "tracker.SendEvent: category and action are required",
			"SendEvent must return an error in case category and action is empty")
	})

	event.Action = "button_press"
	event.Category = "ui_action"

	//Testcase 3
	t.Run("Google Analytics response should not be OK", func(test *testing.T) {
		server := MockServer(400, "")
		tracker.gaAPIURL = server.URL
		resp := tracker.SendEvent(context.Background(), *event)
		assert.NotNil(test, resp, "error can't be nil")
		assert.Equal(test, resp.Error(), "tracker.SendEvent: analytics response status not ok",
			"SendEvent must return an error in case response not ok from google analytics")
	})

	//Testcase 4
	t.Run("Google Analytics response should be OK", func(test *testing.T) {
		server := MockServer(200, "")
		tracker.gaAPIURL = server.URL
		resp := tracker.SendEvent(context.Background(), *event)
		assert.Nil(test, resp, "error must be nil if response is ok from google analytics")
	})
}

func MockServer(status int, encodeValue interface{}) *httptest.Server {
	f := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(encodeValue)
	}

	return httptest.NewServer(http.HandlerFunc(f))
}
