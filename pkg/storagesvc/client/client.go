// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

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
	"strconv"
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
		UploadReader(ctx context.Context, fileName string, r io.Reader, fileSize int64, metadata *map[string]string) (string, error)
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
// masterSecret enables HMAC-SHA256 request signing per the design at
// docs/internal-auth/00-design.md. The master is the chart-installed
// fission-internal-auth/secret value; this client derives the
// per-service signing key for ServiceStoragesvc internally so a
// future leak of one channel's runtime memory cannot forge requests
// on a different channel. Storagesvc only enforces signatures when
// its own copy of the master is set on the server, so passing nil
// (or empty) here is backwards compatible with installs that have
// internalAuth disabled.
//
// Controller pods (storagesvc, buildermgr, the in-pod fetcher binary)
// should pass HMACSecretFromEnv(); CLI commands should pass
// HMACSecretFromCluster() so they read the same Secret the cluster
// uses.
func MakeClient(url string, masterSecret []byte) ClientInterface {
	var rt http.RoundTripper = otelhttp.NewTransport(http.DefaultTransport)
	if len(masterSecret) > 0 {
		rt = hmacauth.ServiceSigner(masterSecret, hmacauth.ServiceStoragesvc, rt, time.Now)
	}
	return MakeClientWithTransport(url, rt)
}

// MakeClientWithTransport is MakeClient with a caller-supplied signing transport.
// Namespace-scoped callers (the per-namespace fetcher) build a transport that
// signs with a derived key and sets the namespace header rather than deriving
// from the master, and pass it here.
func MakeClientWithTransport(url string, rt http.RoundTripper) ClientInterface {
	return &client{
		url:        strings.TrimSuffix(url, "/") + "/v1",
		httpClient: &http.Client{Transport: rt},
	}
}

// namespaceHeaderRoundTripper sets the X-Fission-Auth-Namespace header (the
// tenant the request is scoped to) before delegating to the signing transport.
// The header is not part of the HMAC canonical, so setting it here does not
// affect the signature; it tells storagesvc which namespace's key to verify with.
type namespaceHeaderRoundTripper struct {
	namespace string
	next      http.RoundTripper
}

func (n *namespaceHeaderRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(hmacauth.HeaderNamespace, n.namespace)
	return n.next.RoundTrip(r)
}

// MakeClientNS is MakeClient for a namespace-scoped caller: it signs with the
// per-namespace derived ServiceStoragesvc key (so an archive it uploads is scoped
// to `namespace`, and storagesvc reports `namespace` as the authenticated
// principal) and sets the namespace header. masterSecret is the chart-installed
// master, from which the namespace key is derived in-process. With an empty
// masterSecret (internalAuth disabled) or an empty namespace it is exactly
// MakeClient — unsigned or master-derived/unscoped — so it is always a safe drop-in.
func MakeClientNS(url string, masterSecret []byte, namespace string) ClientInterface {
	if len(masterSecret) == 0 || namespace == "" {
		return MakeClient(url, masterSecret)
	}
	rt := hmacauth.ServiceSignerNS(masterSecret, hmacauth.ServiceStoragesvc, namespace,
		otelhttp.NewTransport(http.DefaultTransport), time.Now)
	return MakeClientWithTransport(url, &namespaceHeaderRoundTripper{namespace: namespace, next: rt})
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
// (nil, nil) when the Secret does not exist (internalAuth disabled in
// the chart) so callers can fall back to unsigned requests; returns
// a descriptive error when the Secret exists but has no usable
// `secret` key (mis-configured or hand-authored Secret) so callers
// don't silently fall back to unsigned and confuse 401 debugging
// downstream.
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
	value, ok := secret.Data[internalAuthSecretKey]
	if !ok || len(value) == 0 {
		return nil, fmt.Errorf("secret %s/%s exists but has no %q key with a non-empty value; either the chart's internalAuth materialisation has been overridden or the Secret was hand-authored",
			namespace, internalAuthSecretName, internalAuthSecretKey)
	}
	return value, nil
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
	return c.UploadReader(ctx, filePath, f, fi.Size(), metadata)
}

// UploadReader sends the contents of r (of length fileSize, named fileName in
// the multipart form) to the storage service and returns the file ID. It is the
// reader-based form of Upload: a caller that has already opened the file — for
// example through an os.Root on the server-side fetch path — passes the open
// file directly so no second, path-based open is needed.
func (c *client) UploadReader(ctx context.Context, fileName string, r io.Reader, fileSize int64, metadata *map[string]string) (string, error) {
	req, err := newUploadRequest(ctx, c.url+"/archive", fileName, r, fileSize)
	if err != nil {
		return "", err
	}

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

// newUploadRequest builds the POST /archive multipart request for fileName +
// the contents of r (fileSize bytes). When r is an io.ReadSeeker (the common
// case — an *os.File), the body is streamed: the multipart envelope is assembled
// around the file without buffering it, and req.GetBody re-streams it (seeking
// back to the start) so the HMAC signer can hash without buffering either.
// A non-seekable r falls back to buffering the whole multipart body in memory.
func newUploadRequest(ctx context.Context, archiveURL, fileName string, r io.Reader, fileSize int64) (*http.Request, error) {
	var (
		req         *http.Request
		contentType string
		err         error
	)

	if rs, ok := r.(io.ReadSeeker); ok {
		start, serr := rs.Seek(0, io.SeekCurrent)
		if serr != nil {
			return nil, serr
		}
		// Assemble the multipart envelope once (part header + closing boundary)
		// using multipart.Writer's own boundary, then stream prefix + file +
		// suffix. The bytes are identical to multipart.Writer's output for that
		// boundary, but the file is never copied into memory.
		var hdr bytes.Buffer
		mw := multipart.NewWriter(&hdr)
		if _, err = mw.CreateFormFile("uploadfile", fileName); err != nil {
			return nil, err
		}
		prefix := append([]byte(nil), hdr.Bytes()...)
		// multipart.Writer.Close() can't emit the closing boundary here because
		// using the Writer would mean io.Copy-ing the file through it (buffering
		// — the exact thing this path avoids), so reconstruct the delimiter by
		// hand. The streamed bytes are asserted byte-identical to the Writer's
		// output in client_request_test.go.
		suffix := []byte("\r\n--" + mw.Boundary() + "--\r\n")
		contentType = mw.FormDataContentType()

		newBody := func() io.ReadCloser {
			return &seekingMultipartBody{prefix: prefix, suffix: suffix, rs: rs, start: start, limit: fileSize}
		}
		if req, err = http.NewRequestWithContext(ctx, http.MethodPost, archiveURL, newBody()); err != nil {
			return nil, err
		}
		req.GetBody = func() (io.ReadCloser, error) { return newBody(), nil }
		req.ContentLength = int64(len(prefix)) + fileSize + int64(len(suffix))
	} else {
		buf := &bytes.Buffer{}
		mw := multipart.NewWriter(buf)
		fw, ferr := mw.CreateFormFile("uploadfile", fileName)
		if ferr != nil {
			return nil, ferr
		}
		if _, ferr = io.Copy(fw, r); ferr != nil {
			return nil, ferr
		}
		contentType = mw.FormDataContentType()
		if ferr = mw.Close(); ferr != nil {
			return nil, ferr
		}
		if req, err = http.NewRequestWithContext(ctx, http.MethodPost, archiveURL, buf); err != nil {
			return nil, err
		}
	}

	// The backend needs the original file size (distinct from the encoded
	// multipart Content-Length); see uploadHandler's X-File-Size handling.
	req.Header.Set("X-File-Size", strconv.FormatInt(fileSize, 10))
	req.Header.Set("Content-Type", contentType)
	return req, nil
}

// seekingMultipartBody streams a single-file multipart body — prefix + the
// seekable file (capped at limit bytes) + closing boundary — seeking the file
// to its captured start offset on the first read. A fresh instance is used for
// req.Body and for each req.GetBody call, so the sign pass and the send pass
// each replay the body from the start without buffering it.
//
// NOT safe for concurrent reads: req.Body and the GetBody instances share one
// underlying *os.File and seek the same offset. Correctness relies on the passes
// being strictly sequential — the signer hashes GetBody to EOF before the
// transport reads req.Body. The streaming round-trip test is the guardrail.
type seekingMultipartBody struct {
	prefix, suffix []byte
	rs             io.ReadSeeker
	start          int64
	limit          int64 // bytes of rs to stream (the stat-time file size)
	mr             io.Reader
}

func (b *seekingMultipartBody) Read(p []byte) (int, error) {
	if b.mr == nil {
		if _, err := b.rs.Seek(b.start, io.SeekStart); err != nil {
			return 0, err
		}
		// Cap the file read to limit so the streamed length always matches the
		// ContentLength derived from it, even if the file grew after stat.
		b.mr = io.MultiReader(bytes.NewReader(b.prefix), io.LimitReader(b.rs, b.limit), bytes.NewReader(b.suffix))
	}
	return b.mr.Read(p)
}

func (b *seekingMultipartBody) Close() error { return nil }

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
