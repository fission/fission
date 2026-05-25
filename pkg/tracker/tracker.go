// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

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

	"github.com/fission/fission/pkg/utils/uuid"
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
	id := uuid.NewString()

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
		cid:          id,
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
	ctx, cancel := context.WithTimeoutCause(req.Context(), HTTP_TIMEOUT, fmt.Errorf("tracker request timeout (%f)s exceeded", HTTP_TIMEOUT.Seconds()))
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
