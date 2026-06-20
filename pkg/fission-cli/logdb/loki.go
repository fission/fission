// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package logdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/fission-cli/console"
	"github.com/fission/fission/pkg/fission-cli/util"
)

// loki is the reference OTLP-backend query adapter (RFC-0016): it queries a
// Loki instance over the HTTP query_range API using LogQL. It assumes function
// logs are labeled per the RFC schema (fission_function_uid /
// fission_function_namespace, etc.), which the OpenTelemetry Collector pipeline
// produces.
const (
	lokiQueryRangePath = "/loki/api/v1/query_range"
	lokiHTTPTimeout    = 30 * time.Second
	// defaultLokiLookback floors the query_range start. The CLI's default
	// Since is the epoch, which would make the range span decades; Loki
	// rejects any range over max_query_length (default 30d) with a 400. Floor
	// the window to a recent span that stays well under that default so a
	// no-bound `fission function logs --dbtype loki` works against a stock
	// Loki. A caller asking for a tighter (more recent) Since is honored.
	defaultLokiLookback = 7 * 24 * time.Hour
)

type loki struct {
	endpoint string // base URL, e.g. http://loki:3100
	client   *http.Client
}

func init() {
	Register(LOKI, func(ctx context.Context, opts LogDBOptions) (LogDatabase, error) {
		return NewLoki(ctx, opts)
	})
}

// NewLoki builds the Loki adapter. It targets LOKI_URL when set; otherwise it
// port-forwards to the in-cluster "loki" service (best-effort — set LOKI_URL to
// reach a Loki running elsewhere).
func NewLoki(ctx context.Context, opts LogDBOptions) (loki, error) {
	endpoint := os.Getenv("LOKI_URL")
	if endpoint == "" {
		localPort, err := util.SetupPortForward(ctx, opts.Client, util.GetFissionNamespace(), "svc=loki")
		if err != nil {
			return loki{}, fmt.Errorf("LOKI_URL not set and port-forward to the loki service failed: %w", err)
		}
		endpoint = "http://127.0.0.1:" + localPort
	}
	return loki{
		endpoint: strings.TrimSuffix(endpoint, "/"),
		client:   &http.Client{Timeout: lokiHTTPTimeout},
	}, nil
}

// buildLogQL turns a LogFilter into a LogQL query: a stream selector on the
// function's labels plus an optional json pipeline narrowing to a request id,
// trace id, or level. Loki requires at least one matcher with a non-empty
// value, so it errors when nothing selectable is set rather than emitting an
// empty matcher Loki would reject with an opaque 400.
func buildLogQL(filter LogFilter) (string, error) {
	var selectors []string
	if filter.FuncUid != "" {
		selectors = append(selectors, fmt.Sprintf("fission_function_uid=%q", filter.FuncUid))
	}
	if filter.PodNamespace != "" {
		selectors = append(selectors, fmt.Sprintf("fission_function_namespace=%q", filter.PodNamespace))
	}
	if filter.Pod != "" {
		selectors = append(selectors, fmt.Sprintf("k8s_pod_name=%q", filter.Pod))
	}
	if len(selectors) == 0 && filter.Function != "" {
		selectors = append(selectors, fmt.Sprintf("fission_function_name=%q", filter.Function))
	}
	if len(selectors) == 0 {
		return "", errors.New("loki query needs at least a function name, uid, namespace, or pod to select on")
	}
	query := "{" + strings.Join(selectors, ", ") + "}"

	var pipeline []string
	if filter.RequestID != "" {
		pipeline = append(pipeline, fmt.Sprintf("fission_request_id=%q", filter.RequestID))
	}
	if filter.TraceID != "" {
		pipeline = append(pipeline, fmt.Sprintf("trace_id=%q", filter.TraceID))
	}
	if filter.Level != "" {
		pipeline = append(pipeline, fmt.Sprintf("level=%q", filter.Level))
	}
	if len(pipeline) > 0 {
		// json parses the structured-metadata/JSON line into labels the
		// downstream matchers filter on.
		query += " | json | " + strings.Join(pipeline, " | ")
	}
	return query, nil
}

// lokiQueryRangeResponse is the subset of Loki's query_range response we read.
type lokiQueryRangeResponse struct {
	Data struct {
		Result []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"` // [ unix_nanos_string, line ]
		} `json:"result"`
	} `json:"data"`
}

func (l loki) GetLogs(ctx context.Context, filter LogFilter, output *bytes.Buffer) error {
	query, err := buildLogQL(filter)
	if err != nil {
		return err
	}
	direction := "forward"
	if filter.Reverse {
		direction = "backward"
	}
	params := url.Values{}
	params.Set("query", query)
	params.Set("limit", strconv.Itoa(filter.RecordLimit))
	params.Set("direction", direction)
	// Floor the start to a recent window: the caller's Since may be the epoch
	// (the CLI default), which Loki rejects as exceeding max_query_length.
	start := filter.Since
	if floor := time.Now().Add(-defaultLokiLookback); start.Before(floor) {
		start = floor
	}
	params.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(time.Now().UnixNano(), 10))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.endpoint+lokiQueryRangePath+"?"+params.Encode(), nil)
	if err != nil {
		return fmt.Errorf("error creating loki request: %w", err)
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return fmt.Errorf("error querying loki: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ferror.MakeErrorFromHTTP(resp)
	}

	var parsed lokiQueryRangeResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("failed to decode loki response: %w", err)
	}

	var entries []LogEntry
	for _, stream := range parsed.Data.Result {
		for _, v := range stream.Values {
			ns, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil {
				// Best-effort: skip one malformed row rather than discard the
				// whole batch of valid lines over a single bad timestamp.
				console.Warn(fmt.Sprintf("skipping loki log entry with invalid timestamp %q: %v", v[0], err))
				continue
			}
			entries = append(entries, LogEntry{
				Timestamp: time.Unix(0, ns).UTC(),
				Message:   strings.TrimSuffix(v[1], "\n"),
				Stream:    stream.Stream["stream"],
				Container: stream.Stream["k8s_container_name"],
				Namespace: stream.Stream["fission_function_namespace"],
				FuncName:  stream.Stream["fission_function_name"],
				FuncUid:   stream.Stream["fission_function_uid"],
				Pod:       stream.Stream["k8s_pod_name"],
			})
		}
	}
	sort.Sort(ByTimestamp(entries, filter.Reverse))

	for _, entry := range entries {
		if err := writeLogEntry(output, entry, filter.Details); err != nil {
			return err
		}
	}
	return nil
}
