package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	uuid "github.com/satori/go.uuid"
	"github.com/stretchr/testify/assert"
)

type request struct {
	tracker *tracker
	event   *Event
}

func TestTracker(t *testing.T) {
	t.Run("NewTracker", func(test *testing.T) {
		for _, test := range []struct {
			name     string
			gaAPIURl string
			expected error
		}{
			{
				name:     "GA Tracking ID should not be empty",
				gaAPIURl: "",
				expected: errors.New("tracker.NewTracker: GA_TRACKING_ID env not set"),
			},
			{
				name:     "Tracker should initialize properly",
				gaAPIURl: "/",
				expected: nil,
			},
		} {
			t.Run(test.name, func(testing *testing.T) {
				if test.expected == nil {
					os.Setenv(GA_TRACKING_ID, "UA-000000-2")
					resp := NewTracker()
					assert.Nil(testing, resp, test.expected)
				} else {
					resp := NewTracker()
					assert.NotNil(testing, resp)
					assert.Equal(testing, resp.Error(), test.expected.Error())
				}
			})
		}
	})

	t.Run("SendEvent", func(test *testing.T) {
		id, err := uuid.NewV4()
		if err != nil {
			log.Fatal(err)
		}

		tracker := &tracker{
			gaPropertyID: "UA-000000-2",
			gaAPIURL:     "/",
			cid:          id.String(),
		}
		for _, test := range []struct {
			name     string
			request  *request
			expected error
			status   int
		}{
			{
				name: "category and action should not be empty",
				request: &request{
					tracker: tracker,
					event: &Event{
						Category: "",
						Action:   "",
						Label:    "play",
						Value:    "value",
					},
				},
				expected: errors.New("tracker.SendEvent: category and action are required"),
				status:   http.StatusInternalServerError,
			},
			{
				name: "Google Analytics response should not be OK",
				request: &request{
					tracker: tracker,
					event: &Event{
						Category: "UI_action",
						Action:   "button_press",
						Label:    "play",
						Value:    "value",
					},
				},
				expected: errors.New("tracker.SendEvent: analytics response status not ok"),
				status:   http.StatusInternalServerError,
			},
			{
				name: "Google Analytics response should be OK",
				request: &request{
					tracker: tracker,
					event: &Event{
						Category: "UI_action",
						Action:   "button_press",
						Label:    "play",
						Value:    "value",
					},
				},
				expected: nil,
				status:   http.StatusOK,
			},
		} {
			t.Run(test.name, func(testing *testing.T) {
				server := MockHTTPServer(test.status, "")
				defer server.Close()
				test.request.tracker.gaAPIURL = server.URL

				resp := test.request.tracker.SendEvent(context.Background(), *test.request.event)
				if test.status == http.StatusOK {
					assert.Nil(testing, resp, test.expected)
				} else {
					assert.NotNil(testing, resp)
					assert.Equal(testing, resp.Error(), test.expected.Error())
				}

			})
		}
	})
}

func MockHTTPServer(status int, encodeValue interface{}) *httptest.Server {
	f := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(encodeValue)
		if err != nil {
			log.Fatal(err)
		}
	}
	return httptest.NewServer(http.HandlerFunc(f))
}
