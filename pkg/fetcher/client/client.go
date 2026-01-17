package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/go-logr/logr"

	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/fetcher"
)

type (
	ClientInterface interface {
		Specialize(context.Context, *fetcher.FunctionSpecializeRequest) error
		Fetch(context.Context, *fetcher.FunctionFetchRequest) error
		Upload(context.Context, *fetcher.ArchiveUploadRequest) (*fetcher.ArchiveUploadResponse, error)
	}
	client struct {
		logger     logr.Logger
		url        string
		httpClient *http.Client
	}
)

func MakeClient(logger logr.Logger, fetcherUrl string) ClientInterface {
	hc := &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}
	return &client{
		logger:     logger.WithName("fetcher_client"),
		url:        strings.TrimSuffix(fetcherUrl, "/"),
		httpClient: hc,
	}
}

func (c *client) getSpecializeUrl() string {
	return c.url + "/specialize"
}

func (c *client) getFetchUrl() string {
	return c.url + "/fetch"
}

func (c *client) getUploadUrl() string {
	return c.url + "/upload"
}

func (c *client) Specialize(ctx context.Context, req *fetcher.FunctionSpecializeRequest) error {
	_, err := sendRequest(c.logger, ctx, c.httpClient, req, c.getSpecializeUrl())
	return err
}

func (c *client) Fetch(ctx context.Context, fr *fetcher.FunctionFetchRequest) error {
	_, err := sendRequest(c.logger, ctx, c.httpClient, fr, c.getFetchUrl())
	return err
}

func (c *client) Upload(ctx context.Context, fr *fetcher.ArchiveUploadRequest) (*fetcher.ArchiveUploadResponse, error) {
	body, err := sendRequest(c.logger, ctx, c.httpClient, fr, c.getUploadUrl())
	if err != nil {
		return nil, err
	}

	uploadResp := fetcher.ArchiveUploadResponse{}
	err = json.Unmarshal(body, &uploadResp)
	if err != nil {
		return nil, err
	}

	return &uploadResp, nil
}

func sendRequest(logger logr.Logger, ctx context.Context, httpClient *http.Client, req any, url string) ([]byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	const (
		maxRetries = 20
		minBackoff = 50 * time.Millisecond
		maxBackoff = 2 * time.Second
	)

	var resp *http.Response
	var lastErr error

	for i := range maxRetries {
		// Check context before request
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err = httpClient.Do(httpReq)

		if err == nil {
			if resp.StatusCode == http.StatusOK {
				respBody, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				if readErr != nil {
					logger.Error(readErr, "error reading response body")
					return nil, readErr
				}
				return respBody, nil
			}

			lastErr = ferror.MakeErrorFromHTTP(resp)
			resp.Body.Close()

			// Don't retry on client errors (4xx), except 429 Too Many Requests
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 429 {
				return nil, lastErr
			}
		} else {
			lastErr = err
		}

		// Check if we should stop retrying
		if i == maxRetries-1 {
			break
		}

		// Check context deadline specifically if it was the cause
		if errors.Is(lastErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			msg := "error specializing function pod, either increase the specialization timeout for function or check function pod log would help."
			wrappedErr := fmt.Errorf("%s: %w", msg, lastErr)
			logger.Error(lastErr, msg, "url", url)
			return nil, wrappedErr
		}

		// Exponential backoff with cap
		backoff := min(time.Duration(50*(1<<i))*time.Millisecond, maxBackoff)

		logger.Info("retrying request",
			"url", url,
			"attempt", i+1,
			"backoff", backoff,
			"error", lastErr.Error())
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
			continue
		}
	}

	logger.Error(lastErr, "request failed after max retries", "url", url, "attempts", maxRetries)
	return nil, lastErr
}
