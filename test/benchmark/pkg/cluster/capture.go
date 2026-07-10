// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Capturer collects server-side signals alongside client-side load:
// Prometheus queries and pprof snapshots. Both are optional — when a URL is
// unset the corresponding capture is a no-op so the same scenario code runs
// against clusters with or without observability wired up.
type Capturer struct {
	// PrometheusURL is the base URL of a Prometheus that scrapes the Fission
	// control plane (e.g. http://127.0.0.1:9090). Empty disables PromQL capture.
	PrometheusURL string
	// PprofTargets maps a service label to its pprof base URL
	// (e.g. {"router": "http://127.0.0.1:6060"}). Empty disables pprof capture.
	PprofTargets map[string]string
	// HTTP is the client used for both; defaults to a 30s client when nil.
	HTTP *http.Client
}

func (c *Capturer) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// PrometheusEnabled reports whether PromQL capture is configured.
func (c *Capturer) PrometheusEnabled() bool { return c != nil && c.PrometheusURL != "" }

// QueryInstant runs an instant PromQL query and returns the first sample value.
// found is false when Prometheus is not configured or the query yields no
// samples — distinct from a genuine zero value, so callers can tell a real
// metric of 0 apart from an absent one.
func (c *Capturer) QueryInstant(ctx context.Context, promQL string) (value float64, found bool, err error) {
	if !c.PrometheusEnabled() {
		return 0, false, nil
	}
	q := url.Values{"query": {promQL}}
	body, err := c.get(ctx, c.PrometheusURL+"/api/v1/query?"+q.Encode())
	if err != nil {
		return 0, false, err
	}
	var resp struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value [2]any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, false, fmt.Errorf("decode prometheus response: %w", err)
	}
	if resp.Status != "success" {
		return 0, false, fmt.Errorf("prometheus query status %q", resp.Status)
	}
	if len(resp.Data.Result) == 0 {
		return 0, false, nil
	}
	s, ok := resp.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, false, fmt.Errorf("unexpected prometheus value type %T", resp.Data.Result[0].Value[1])
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false, err
	}
	// PromQL yields NaN for 0/0 rate ratios (and ±Inf for x/0); treat those as
	// "no sample" — a non-finite value poisons the results JSON (json.Marshal
	// rejects NaN), losing the entire run's output.
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false, nil
	}
	return v, true, nil
}

// QueryRangeRaw runs a range query and returns the raw JSON response so it can
// be persisted as an artifact for offline analysis.
func (c *Capturer) QueryRangeRaw(ctx context.Context, promQL string, start, end time.Time, step time.Duration) ([]byte, error) {
	if !c.PrometheusEnabled() {
		return nil, nil
	}
	q := url.Values{
		"query": {promQL},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)},
	}
	return c.get(ctx, c.PrometheusURL+"/api/v1/query_range?"+q.Encode())
}

// SnapshotPprof writes a heap and goroutine profile for every configured pprof
// target into outDir, prefixing files with label (e.g. "before"/"after"). It
// best-effort-skips targets that are unreachable, returning the first error only
// after attempting all of them.
func (c *Capturer) SnapshotPprof(ctx context.Context, label, outDir string) error {
	if c == nil || len(c.PprofTargets) == 0 {
		return nil
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	var firstErr error
	for svc, base := range c.PprofTargets {
		for _, p := range []struct{ name, path string }{
			{"heap", "/debug/pprof/heap"},
			{"goroutine", "/debug/pprof/goroutine?debug=1"},
		} {
			body, err := c.get(ctx, base+p.path)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("pprof %s %s: %w", svc, p.name, err)
				}
				continue
			}
			file := filepath.Join(outDir, fmt.Sprintf("%s-%s-%s.pprof", label, svc, p.name))
			if err := os.WriteFile(file, body, 0o644); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (c *Capturer) get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", rawURL, resp.StatusCode)
	}
	return body, nil
}
