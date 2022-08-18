/*
Copyright 2021 The Fission Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package tracker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	uuid "github.com/satori/go.uuid"
)

const (
	HTTP_TIMEOUT   time.Duration = 5 * time.Second
	GA_TRACKING_ID string        = "GA_TRACKING_ID"
	GA_API_URL     string        = "GA_API_URL"
)

type (
	Tracker struct {
		gaPropertyID string
		cid          string
		gaAPIURL     string
	}
	Event struct {
		Category string
		Action   string
		Label    string
		Value    string
	}
)

func NewTracker() (*Tracker, error) {
	id, err := uuid.NewV4()
	if err != nil {
		return nil, fmt.Errorf("tracker.NewTracker: error generating UUID: %w", err)
	}

	gaTrackingID := os.Getenv(GA_TRACKING_ID)
	if gaTrackingID == "" {
		return nil, errors.New("tracker.NewTracker: GA_TRACKING_ID env not set")
	}

	gaAPIURL := os.Getenv(GA_API_URL)
	if gaAPIURL == "" {
		gaAPIURL = "https://www.google-analytics.com/collect"
	}

	tracker := &Tracker{
		gaPropertyID: gaTrackingID,
		cid:          id.String(),
		gaAPIURL:     gaAPIURL,
	}
	return tracker, nil
}

func (t *Tracker) SendEvent(ctx context.Context, e Event) error {
	if e.Action == "" || e.Category == "" {
		return errors.New("tracker.SendEvent: category and action are required")
	}

	v := url.Values{
		"v":   {"1"},
		"tid": {t.gaPropertyID},
		"cid": {t.cid},
		"t":   {"event"},
		"ec":  {e.Category},
		"ea":  {e.Action},
	}

	if e.Label != "" {
		v.Add("el", e.Label)
	}

	if e.Value != "" {
		v.Add("ev", e.Value)
	}

	buf := bytes.NewBufferString(v.Encode())
	req, err := http.NewRequestWithContext(ctx, "POST", t.gaAPIURL, buf)
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add("User-Agent", "ga-tracker/1.0")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(req.Context(), HTTP_TIMEOUT)
	defer cancel()

	req = req.WithContext(ctx)

	client := http.DefaultClient
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New("tracker.SendEvent: analytics response status not ok")
	}
	defer resp.Body.Close()
	return err
}
