// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sanketsudake/go-portless/backend"
	"golang.org/x/net/context/ctxhttp"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/uuid"
)

const (
	ENV_DISABLE_OWNER_REFERENCES string = "DISABLE_OWNER_REFERENCES"
)

func UrlForFunction(name, namespace string) string {
	prefix := "/fission-function"
	if namespace != metav1.NamespaceDefault {
		prefix = fmt.Sprintf("/fission-function/%s", namespace)
	}
	return fmt.Sprintf("%s/%s", prefix, name)
}

// UrlForFunctionRef is UrlForFunction with a router alias/version suffix
// (":<suffix>") appended when suffix is non-empty -- the same "name:tag"
// grammar the router's internal `:<alias>`/`:<version>` routes register at
// (pkg/router/routeshape.go). Callers pass a FunctionReference's Alias if
// set, else its Version (the two are mutually exclusive -- CEL/webhook
// enforced, see FunctionReference's doc comment in pkg/apis/core/v1/types.go);
// a bare reference (neither set) gets exactly UrlForFunction's result.
//
// This exists so every RFC-0025-aware publisher (timer, kubewatcher,
// mqtrigger's kafka/statestore/scalermanager backends) builds the suffixed
// URL the same one way, rather than five copies of the same
// `if suffix != "" { url += ":" + suffix }` -- resolution of what the suffix
// actually routes to stays entirely router-side; publishers only ever build
// a URL string.
func UrlForFunctionRef(name, namespace, suffix string) string {
	url := UrlForFunction(name, namespace)
	if suffix != "" {
		url += ":" + suffix
	}
	return url
}

// UrlForFunctionReference is UrlForFunctionRef with the alias/version suffix
// selection folded in: it reads ref.Alias, falling back to ref.Version, and
// builds the URL in one call. The two fields are mutually exclusive
// (CEL/webhook enforced -- see FunctionReference's doc comment in
// pkg/apis/core/v1/types.go), so this is the one place that picks between
// them; every RFC-0025-aware publisher (timer, kubewatcher, mqtrigger's
// kafka/statestore/scalermanager backends) should call this instead of
// repeating the alias-else-version selection at the call site.
func UrlForFunctionReference(ref fv1.FunctionReference, namespace string) string {
	suffix := ref.Alias
	if suffix == "" {
		suffix = ref.Version
	}
	return UrlForFunctionRef(ref.Name, namespace, suffix)
}

// GetFunctionIstioServiceName return service name of function for istio feature
func GetFunctionIstioServiceName(fnName, fnNamespace string) string {
	return fmt.Sprintf("istio-%s-%s", fnName, fnNamespace)
}

// GetTempDir creates and return a temporary directory
func GetTempDir() (string, error) {
	tmpDir := uuid.NewString()
	dir, err := os.MkdirTemp("", tmpDir)
	return dir, err
}

// FindAllGlobs returns a list of globs of input list.
func FindAllGlobs(paths ...string) ([]string, error) {
	files := make([]string, 0)
	for _, p := range paths {
		// use absolute path to find files
		path, err := filepath.Abs(p)
		if err != nil {
			return nil, fmt.Errorf("error getting absolute path of path '%s': %w", p, err)
		}
		globs, err := filepath.Glob(path)
		if err != nil {
			return nil, fmt.Errorf("invalid glob %s: %s", path, err)
		}
		files = append(files, globs...)
		// xxx handle excludeGlobs here
	}
	return files, nil
}

// RemoveZeroBytes remove empty byte(\x00) from input byte slice and return a new byte slice
// This function is trying to fix the problem that empty byte will fail os.Openfile
// For more information, please visit:
// 1. https://github.com/golang/go/issues/24195
// 2. https://play.golang.org/p/5F9ykC2tlbc
func RemoveZeroBytes(src []byte) []byte {
	var bs []byte
	for _, v := range src {
		if v != 0 {
			bs = append(bs, v)
		}
	}
	return bs
}

// GetImagePullPolicy returns the image pull policy base on the input value.
func GetImagePullPolicy(policy string) apiv1.PullPolicy {
	switch policy {
	case "Always":
		return apiv1.PullAlways
	case "Never":
		return apiv1.PullNever
	default:
		return apiv1.PullIfNotPresent
	}
}

func FileSize(filePath string) (int64, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return 0, err
	}
	return info.Size(), err
}

func GetFileChecksum(fileName string) (*fv1.Checksum, error) {
	f, err := os.Open(fileName)
	if err != nil {
		return nil, fmt.Errorf("failed to open file %v: %w", fileName, err)
	}
	defer f.Close()

	sum, err := GetChecksum(f)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate checksum for %v", fileName)
	}

	return sum, nil
}

// RootFileChecksum is GetFileChecksum with fileName opened through an os.Root
// rooted at base, so a request-derived path cannot read a file outside base
// (CWE-22). Use this on the server-side fetch path.
func RootFileChecksum(base, fileName string) (*fv1.Checksum, error) {
	f, err := RootOpen(base, fileName)
	if err != nil {
		return nil, fmt.Errorf("failed to open file %v: %w", fileName, err)
	}
	defer f.Close()

	sum, err := GetChecksum(f)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate checksum for %v", fileName)
	}

	return sum, nil
}

func GetChecksum(src io.Reader) (*fv1.Checksum, error) {
	if src == nil {
		return nil, errors.New("cannot read from nil reader")
	}

	h := sha256.New()

	_, err := io.Copy(h, src)
	if err != nil {
		return nil, err
	}

	return &fv1.Checksum{
		Type: fv1.ChecksumTypeSHA256,
		Sum:  hex.EncodeToString(h.Sum(nil)),
	}, nil
}

func IsURL(str string) bool {
	return strings.HasPrefix(str, "http://") || strings.HasPrefix(str, "https://")
}

func isHttp2xxSuccessful(status int) bool {
	return status >= 200 && status < 300
}

// DownloadUrl downloads a file from the specified URL and saves it to the local path.
func DownloadUrl(ctx context.Context, httpClient *http.Client, targetURL string, localPath string) error {
	// check valid URL
	if targetURL == "" {
		return errors.New("empty URL")
	}
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %s: %w", targetURL, err)
	}

	// validate local path for directory traversal attacks
	if filepath.Clean(localPath) != localPath {
		return fmt.Errorf("invalid local path: %s", localPath)
	}
	resp, err := ctxhttp.Get(ctx, httpClient, parsed.String())
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if !isHttp2xxSuccessful(resp.StatusCode) {
		return errors.New(resp.Status)
	}

	w, err := os.OpenFile(localPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer w.Close()

	// Force 0o600 even on the overwrite-existing-file path. OpenFile's mode
	// argument is honoured only on file creation; if localPath pre-existed
	// with a broader mode (an attacker could pre-create with 0o644), the
	// file would retain that mode after truncation. fchmod via the open fd
	// closes that window before any data is written.
	if err = w.Chmod(0o600); err != nil {
		return err
	}

	if _, err = io.Copy(w, resp.Body); err != nil {
		return err
	}

	// flushing write buffer to file
	if err = w.Sync(); err != nil {
		return err
	}

	return nil
}

// DownloadUrlToRoot is DownloadUrl with the destination opened through an
// os.Root rooted at base, so a request-derived localPath cannot write outside
// base (CWE-22). Use this on the server-side fetch path. As in DownloadUrl, the
// destination file is created only after a 2xx response, so a failed download
// leaves no partial file behind.
func DownloadUrlToRoot(ctx context.Context, httpClient *http.Client, targetURL string, base string, localPath string) error {
	if targetURL == "" {
		return errors.New("empty URL")
	}
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %s: %w", targetURL, err)
	}
	resp, err := ctxhttp.Get(ctx, httpClient, parsed.String())
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if !isHttp2xxSuccessful(resp.StatusCode) {
		return errors.New(resp.Status)
	}

	w, err := RootOpenFile(base, localPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer w.Close()

	// Force 0o600 even on the overwrite path, for the same reason as DownloadUrl.
	if err = w.Chmod(0o600); err != nil {
		return err
	}

	if _, err = io.Copy(w, resp.Body); err != nil {
		return err
	}

	return w.Sync()
}

func GetStringValueFromEnv(envVar string) (string, error) {
	v := os.Getenv(envVar)
	if v == "" {
		return v, fmt.Errorf("environment variable %s empty", envVar)
	}
	return v, nil
}

func GetUIntValueFromEnv(envVar string) (uint, error) {
	s, err := GetStringValueFromEnv(envVar)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint(value), nil
}

func GetIntValueFromEnv(envVar string) (int, error) {
	s, err := GetStringValueFromEnv(envVar)
	if err != nil {
		return 0, err
	}
	value, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	return value, nil
}

// FindFreePort picks a free TCP port. Callers needing several distinct
// ports at once should use go-portless's backend.ReservePorts directly —
// two sequential FindFreePort calls can return the same port.
func FindFreePort() (int, error) {
	ports, err := backend.ReservePorts(1)
	if err != nil {
		return 0, err
	}
	return ports[0], nil
}

// DeleteOldPackages deletes src and built deployment packages from builder's storage.
// The function also verifies that sharedVolumePath for builder and fetcher containers
// is /packages. A source_package contains a directory and a .tmp file while a deployment
// package contains a directory and a .zip file.
func DeleteOldPackages(pkgPath, pkgType string) error {
	sharedVolumePath := "/packages"
	if !strings.HasPrefix(pkgPath, sharedVolumePath) {
		return fmt.Errorf("invalid shared volume path: %s", pkgPath)
	}

	var file string
	switch pkgType {
	case "DEPLOY_PKG":
		file = pkgPath + ".zip"
	case "SRC_PKG":
		file = pkgPath + ".tmp"
	}

	err := os.RemoveAll(pkgPath)
	if err != nil {
		return err
	}
	err = os.RemoveAll(file)
	if err != nil {
		return err
	}

	return nil
}

func IsOwnerReferencesEnabled() bool {
	disableOwnerReference, _ := strconv.ParseBool(os.Getenv(ENV_DISABLE_OWNER_REFERENCES))
	return !disableOwnerReference
}
