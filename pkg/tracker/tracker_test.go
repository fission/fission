package tracker

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTracker(t *testing.T) {
	ctx := t.Context()
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
					t, err := NewTracker()
					require.Nil(testing, err)
					require.NotNil(testing, t)
				} else {
					t, err := NewTracker()
					require.Nil(testing, t)
					require.NotNil(testing, err)
					require.Equal(testing, err.Error(), test.expected.Error())
				}
			})
		}
	})

	t.Run("SendEvent", func(test *testing.T) {

		os.Setenv(GA_TRACKING_ID, "UA-000000-2")
		tr, err := NewTracker()
		require.NoError(t, err)
		for _, test := range []struct {
			name     string
			request  *Event
			expected error
			status   int
		}{
			{
				name: "category and action should not be empty",
				request: &Event{
					Category: "",
					Action:   "",
					Label:    "play",
					Value:    "value",
				},
				expected: errors.New("tracker.SendEvent: category and action are required"),
				status:   http.StatusInternalServerError,
			},
			{
				name: "Google Analytics response should not be OK",
				request: &Event{
					Category: "UI_action",
					Action:   "button_press",
					Label:    "play",
					Value:    "value",
				},

				expected: errors.New("tracker.SendEvent: analytics response status not ok"),
				status:   http.StatusInternalServerError,
			},
			{
				name: "Google Analytics response should be OK",
				request: &Event{
					Category: "UI_action",
					Action:   "button_press",
					Label:    "play",
					Value:    "value",
				},

				expected: nil,
				status:   http.StatusOK,
			},
		} {
			t.Run(test.name, func(testing *testing.T) {
				server := MockHTTPServer(test.status, "")
				defer server.Close()
				tr.gaAPIURL = server.URL

				t := tr.SendEvent(ctx, *test.request)
				if test.status == http.StatusOK {
					require.Nil(testing, t, test.expected)
				} else {
					require.NotNil(testing, t)
					require.Equal(testing, t.Error(), test.expected.Error())
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
