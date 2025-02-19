package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
	"golang.org/x/net/context/ctxhttp"

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
		logger     *zap.Logger
		url        string
		httpClient *http.Client
	}
)

func MakeClient(logger *zap.Logger, fetcherUrl string) ClientInterface {
	hc := &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}
	return &client{
		logger:     logger.Named("fetcher_client"),
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

func sendRequest(logger *zap.Logger, ctx context.Context, httpClient *http.Client, req interface{}, url string) ([]byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	maxRetries := 20
	var resp *http.Response

	for i := 0; i < maxRetries; i++ {
		resp, err = ctxhttp.Post(ctx, httpClient, url, "application/json", bytes.NewReader(body))

		if err == nil {
			if resp.StatusCode == 200 {
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					logger.Error("error reading response body", zap.Error(err))
				}
				defer resp.Body.Close()
				return body, err
			}
			err = ferror.MakeErrorFromHTTP(resp)
		}

		// skip retry and return directly due to context deadline exceeded
		if err == context.DeadlineExceeded {
			msg := "error specializing function pod, either increase the specialization timeout for function or check function pod log would help."
			err = fmt.Errorf("%s: %w", msg, err)
			logger.Error(msg, zap.Error(err), zap.String("url", url))
			return nil, err
		}

		if i < maxRetries-1 {
			time.Sleep(50 * time.Duration(2*i) * time.Millisecond)
			logger.Error("error specializing/fetching/uploading package, retrying", zap.Error(err), zap.String("url", url))
			continue
		}
	}

	return nil, err
}
