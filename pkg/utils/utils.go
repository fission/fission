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

package utils

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pkg/errors"
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

// IsNetworkError returns true if an error is a network error, and false otherwise.
func IsNetworkError(err error) bool {
	_, ok := err.(net.Error)
	return ok
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
			return nil, errors.Wrapf(err, "error getting absolute path of path '%s'", p)
		}
		globs, err := filepath.Glob(path)
		if err != nil {
			return nil, errors.Errorf("invalid glob %s: %s", path, err)
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
		return nil, fmt.Errorf("failed to open file %v: %v", fileName, err)
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

func DownloadUrl(ctx context.Context, httpClient *http.Client, url string, localPath string) error {
	// validate local path for directory traversal attacks
	if filepath.Clean(localPath) != localPath {
		return errors.Errorf("invalid local path: %s", localPath)
	}
	resp, err := ctxhttp.Get(ctx, httpClient, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if !isHttp2xxSuccessful(resp.StatusCode) {
		return errors.New(resp.Status)
	}

	w, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer w.Close()

	_, err = io.Copy(w, resp.Body)
	if err != nil {
		return err
	}

	// flushing write buffer to file
	err = w.Sync()
	if err != nil {
		return err
	}

	err = os.Chmod(localPath, 0600)
	if err != nil {
		return err
	}

	return nil
}

func GetStringValueFromEnv(envVar string) (string, error) {
	v := os.Getenv(envVar)
	if v == "" {
		return v, errors.New(fmt.Sprintf("Еnvironment variable %s empty", envVar))
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

func FindFreePort() (int, error) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}

	port := listener.Addr().(*net.TCPAddr).Port

	err = listener.Close()
	if err != nil {
		return 0, err
	}

	return port, nil
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
	if pkgType == "DEPLOY_PKG" {
		file = pkgPath + ".zip"
	} else if pkgType == "SRC_PKG" {
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

// SanitizeFilePath checks if the path is valid to prevent directory traversal attacks.
func SanitizeFilePath(path string, safedir string) (string, error) {
	if len(path) == 0 {
		return "", errors.New("invalid path")
	}
	if len(safedir) == 0 {
		return "", errors.New("invalid safe directory")
	}
	// get normalized path and check for directory traversal attacks
	normalizedPath := filepath.Clean(path)
	if normalizedPath != path {
		return "", errors.New("invalid path")
	}
	// check if the path is under the safe directory
	if !strings.HasPrefix(normalizedPath, safedir) {
		return "", errors.New("path is not under safe directory")
	}
	return normalizedPath, nil
}
