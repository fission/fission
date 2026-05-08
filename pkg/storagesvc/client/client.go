/*
Copyright 2016 The Fission Authors.

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

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/net/context/ctxhttp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/storagesvc"
)

// internalAuthSecretName is the chart-installed Secret that holds the
// shared HMAC key used for internal control-plane authentication. The
// chart's name is fixed; see charts/fission-all/templates/internal-auth-secret.yaml.
const internalAuthSecretName = "fission-internal-auth"

// internalAuthSecretKey is the data key inside that Secret.
const internalAuthSecretKey = "secret"

// internalAuthEnv is the env var that controller pods read to obtain
// the HMAC secret. The chart mounts it as a secretKeyRef.
const internalAuthEnv = "FISSION_INTERNAL_AUTH_SECRET"

type (
	ClientInterface interface {
		Upload(ctx context.Context, filePath string, metadata *map[string]string) (string, error)
		GetUrl(id string) string
		Info(ctx context.Context, id string) (*http.Response, error)
		List(ctx context.Context) ([]string, error)
		Download(ctx context.Context, id string, filePath string) error
		GetFile(ctx context.Context, id string) (*http.Response, error)
		Delete(ctx context.Context, id string) error
	}
	client struct {
		url        string
		httpClient *http.Client
	}
)

// MakeClient creates a storage service client.
//
// hmacSecret enables HMAC-SHA256 request signing per the design at
// docs/internal-auth/00-design.md. Storagesvc only enforces signatures
// when its own copy of the secret is set on the server, so passing nil
// (or empty) here is backwards compatible with installs that have
// internalAuth disabled.
//
// Controller pods (storagesvc, buildermgr, the in-pod fetcher binary)
// should pass HMACSecretFromEnv(); CLI commands should pass
// HMACSecretFromCluster() so they read the same Secret the cluster
// uses.
func MakeClient(url string, hmacSecret []byte) ClientInterface {
	var rt http.RoundTripper = otelhttp.NewTransport(http.DefaultTransport)
	if len(hmacSecret) > 0 {
		rt = hmacauth.NewSigner(hmacSecret, rt, time.Now)
	}
	hc := &http.Client{Transport: rt}
	return &client{
		url:        strings.TrimSuffix(url, "/") + "/v1",
		httpClient: hc,
	}
}

// HMACSecretFromEnv returns the HMAC secret from
// FISSION_INTERNAL_AUTH_SECRET. Returns nil when unset, which leaves
// the storagesvc client unsigned — the correct backwards-compatible
// default for installs that have internalAuth disabled.
func HMACSecretFromEnv() []byte {
	if s := os.Getenv(internalAuthEnv); s != "" {
		return []byte(s)
	}
	return nil
}

// HMACSecretFromCluster reads the HMAC secret from the
// fission-internal-auth Secret in the install namespace. Returns
// (nil, nil) when the secret does not exist (internalAuth disabled in
// the chart) so callers can fall back to unsigned requests; returns
// the error for any other failure.
func HMACSecretFromCluster(ctx context.Context, kubeClient kubernetes.Interface, namespace string) ([]byte, error) {
	if kubeClient == nil {
		return nil, nil
	}
	secret, err := kubeClient.CoreV1().Secrets(namespace).Get(ctx, internalAuthSecretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return secret.Data[internalAuthSecretKey], nil
}

// Upload sends the local file pointed to by filePath to the storage
// service, along with the metadata.  It returns a file ID that can be
// used to retrieve the file.
func (c *client) Upload(ctx context.Context, filePath string, metadata *map[string]string) (string, error) {
	// Open first, then stat the file descriptor — this avoids a TOCTOU
	// between a path-based Stat and a subsequent path-based Open if the
	// caller's filesystem is shared with another process.
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	fileSize := fi.Size()

	buf := &bytes.Buffer{}
	bodyWriter := multipart.NewWriter(buf)
	fileWriter, err := bodyWriter.CreateFormFile("uploadfile", filePath)
	if err != nil {
		return "", err
	}

	_, err = io.Copy(fileWriter, f)
	if err != nil {
		return "", err
	}

	contentType := bodyWriter.FormDataContentType()
	bodyWriter.Close()

	req, err := http.NewRequest(http.MethodPost, c.url+"/archive", buf)
	if err != nil {
		return "", err
	}
	req.Header["X-File-Size"] = []string{fmt.Sprintf("%v", fileSize)}
	req.Header["Content-Type"] = []string{contentType}

	resp, err := ctxhttp.Do(ctx, c.httpClient, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("Upload error %v", resp.Status)
		return "", errors.New(msg)
	}

	var ur storagesvc.UploadResponse
	err = json.Unmarshal(body, &ur)
	if err != nil {
		return "", err
	}

	return ur.ID, nil
}

// GetUrl returns an HTTP URL that can be used to download the file pointed to by ID
func (c *client) GetUrl(id string) string {
	return fmt.Sprintf("%v/archive?id=%v", c.url, url.PathEscape(id))
}

func (c *client) List(ctx context.Context) ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, c.url+"/archive", nil)
	if err != nil {
		return []string{}, err
	}
	resp, err := ctxhttp.Do(ctx, c.httpClient, req)
	if err != nil {
		return []string{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return []string{}, err
	}
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("List error %v", resp.Status)
		return []string{}, errors.New(msg)
	}

	var ids []string
	err = json.Unmarshal(body, &ids)
	if err != nil {
		return []string{}, err
	}
	return ids, nil
}

// Download fetches the file identified by ID to the local file path.
// filePath must not exist.
func (c *client) Download(ctx context.Context, id string, filePath string) error {
	// url for id
	url := c.GetUrl(id)

	// O_EXCL atomically requires that the file does not already exist —
	// no separate Stat-then-Create that an attacker could race against by
	// creating filePath in between.
	f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("file already exists: %v", filePath)
		}
		return err
	}
	defer f.Close()

	// make request
	resp, err := ctxhttp.Get(ctx, c.httpClient, url)
	if err != nil {
		os.Remove(filePath)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("HTTP error %v", resp.StatusCode)
		os.Remove(filePath)
		return errors.New(msg)
	}

	// download and write data
	_, err = io.Copy(f, resp.Body)
	if err != nil {
		return err
	}

	return nil
}

// Info issues a HEAD request for the archive identified by id. The
// response carries the X-FISSION-STORAGETYPE / X-FISSION-BUCKET headers
// the CLI uses to decide whether to render a local URL or an S3 URL.
// The caller is responsible for closing the response body.
func (c *client) Info(ctx context.Context, id string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodHead, c.GetUrl(id), nil)
	if err != nil {
		return nil, err
	}
	return ctxhttp.Do(ctx, c.httpClient, req)
}

// Download fetches the file identified by ID to the local file path.
// filePath must not exist.
func (c *client) GetFile(ctx context.Context, id string) (resp *http.Response, err error) {
	// url for id
	url := c.GetUrl(id)

	// make request
	resp, err = ctxhttp.Get(ctx, c.httpClient, url)
	if err != nil {
		return resp, err
	}

	return resp, err
}

func (c *client) Delete(ctx context.Context, id string) error {
	url := c.GetUrl(id)

	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}

	resp, err := ctxhttp.Do(ctx, c.httpClient, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP error %v", resp.StatusCode)
	}

	return nil
}
