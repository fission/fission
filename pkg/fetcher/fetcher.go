/*
Copyright 2019 The Fission Authors.

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

package fetcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/net/context/ctxhttp"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/tools/reference"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/crd"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/error/network"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/info"
	storageSvcClient "github.com/fission/fission/pkg/storagesvc/client"
	"github.com/fission/fission/pkg/utils"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

type (
	Fetcher struct {
		logger           logr.Logger
		sharedVolumePath string
		sharedSecretPath string
		sharedConfigPath string
		fissionClient    versioned.Interface
		kubeClient       kubernetes.Interface
		httpClient       *http.Client
		Info             PodInfo
	}
	PodInfo struct {
		Name      string
		Namespace string
	}
)

func makeVolumeDir(dirPath string) error {
	return os.MkdirAll(dirPath, os.ModeDir|0750)
}

func MakeFetcher(logger logr.Logger, clientGen crd.ClientGeneratorInterface, sharedVolumePath string, sharedSecretPath string,
	sharedConfigPath string, podInfoMountDir string) (*Fetcher, error) {
	fLogger := logger.WithName("fetcher")
	err := makeVolumeDir(sharedVolumePath)
	if err != nil {
		fLogger.Error(err, "error creating shared volume directory", "directory", sharedVolumePath)
	}
	err = makeVolumeDir(sharedSecretPath)
	if err != nil {
		fLogger.Error(err, "error creating shared secret directory", "directory", sharedSecretPath)
	}
	err = makeVolumeDir(sharedConfigPath)
	if err != nil {
		fLogger.Error(err, "error creating shared config directory", "directory", sharedConfigPath)
	}

	fissionClient, err := clientGen.GetFissionClient()
	if err != nil {
		return nil, fmt.Errorf("error making the fission client: %w", err)
	}
	kubeClient, err := clientGen.GetKubernetesClient()
	if err != nil {
		return nil, fmt.Errorf("error making the kube client: %w", err)
	}

	name, err := os.ReadFile(podInfoMountDir + "/name")
	if err != nil {
		return nil, fmt.Errorf("error reading pod name from downward volume: %w", err)
	}

	namespace, err := os.ReadFile(podInfoMountDir + "/namespace")
	if err != nil {
		return nil, fmt.Errorf("error reading pod namespace from downward volume: %w", err)
	}

	hc := &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}
	return &Fetcher{
		logger:           fLogger,
		sharedVolumePath: sharedVolumePath,
		sharedSecretPath: sharedSecretPath,
		sharedConfigPath: sharedConfigPath,
		fissionClient:    fissionClient,
		kubeClient:       kubeClient,
		Info: PodInfo{
			Name:      string(name),
			Namespace: string(namespace),
		},
		httpClient: hc,
	}, nil
}

func verifyChecksum(fileChecksum, checksum *fv1.Checksum) error {
	if checksum.Type != fv1.ChecksumTypeSHA256 {
		return ferror.MakeError(ferror.ErrorInvalidArgument, "Unsupported checksum type")
	}
	if fileChecksum.Sum != checksum.Sum {
		return ferror.MakeError(ferror.ErrorChecksumFail, "Checksum validation failed")
	}
	return nil
}

func writeSecretOrConfigMap(dataMap map[string][]byte, dirPath string) error {
	for key, val := range dataMap {
		writeFilePath := filepath.Join(dirPath, key)
		err := os.WriteFile(writeFilePath, val, 0750)
		if err != nil {
			return fmt.Errorf("failed to write file %s: %w", writeFilePath, err)
		}
	}
	return nil
}

func (fetcher *Fetcher) VersionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, err := w.Write([]byte(info.BuildInfo().String()))
	if err != nil {
		fetcher.logger.Error(err, "error writing response")
	}
}

func (fetcher *Fetcher) FetchHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != "POST" {
		http.Error(w, "only POST is supported on this endpoint", http.StatusMethodNotAllowed)
		return
	}

	logger := otelUtils.LoggerWithTraceID(ctx, fetcher.logger)

	startTime := time.Now()
	defer func() {
		elapsed := time.Since(startTime)
		logger.Info("fetch request done", "elapsed_time", elapsed)
	}()

	// parse request
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Error(err, "error reading request body")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var req FunctionFetchRequest
	err = json.Unmarshal(body, &req)
	if err != nil {
		logger.Error(err, "error parsing request body")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	pkg, err := fetcher.getPkgInformation(ctx, req)
	if err != nil {
		logger.Error(err, "error getting package information")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	code, err := fetcher.Fetch(ctx, pkg, req)
	if err != nil {
		logger.Error(err, "error fetching")
		http.Error(w, err.Error(), code)
		return
	}

	logger.Info("checking secrets/cfgmaps")
	code, err = fetcher.FetchSecretsAndCfgMaps(ctx, req.Secrets, req.ConfigMaps)
	if err != nil {
		logger.Error(err, "error fetching secrets and config maps")
		http.Error(w, err.Error(), code)
		return
	}

	logger.Info("completed fetch request")
	// all done
	w.WriteHeader(http.StatusOK)
}

func (fetcher *Fetcher) SpecializeHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	if r.Method != "POST" {
		http.Error(w, fmt.Sprintf("only POST is supported on this endpoint, %v received", r.Method), http.StatusMethodNotAllowed)
		return
	}
	logger := otelUtils.LoggerWithTraceID(ctx, fetcher.logger)

	// parse request
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Error(err, "error reading request body")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var req FunctionSpecializeRequest
	err = json.Unmarshal(body, &req)
	if err != nil {
		logger.Error(err, "error parsing request body")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	code, err := fetcher.SpecializePod(ctx, req.FetchReq, req.LoadReq)
	if err != nil {
		logger.Error(err, "error specializing pod", "statusCode", code)
		http.Error(w, err.Error(), code)
		return
	}

	// all done
	w.WriteHeader(http.StatusOK)
}

// Fetch takes FetchRequest and makes the fetch call
// It returns the HTTP code and error if any
func (fetcher *Fetcher) Fetch(ctx context.Context, pkg *fv1.Package, req FunctionFetchRequest) (int, error) {
	logger := otelUtils.LoggerWithTraceID(ctx, fetcher.logger)

	storePath, err := utils.SanitizeFilePath(filepath.Join(fetcher.sharedVolumePath, req.Filename), fetcher.sharedVolumePath)
	if err != nil {
		logger.Error(err, "filename", req.Filename)
		return http.StatusBadRequest, fmt.Errorf("%s, request: %v", err, req)
	}

	// verify first if the file already exists.
	if _, err := os.Stat(storePath); err == nil {
		logger.Info("requested file already exists at shared volume - skipping fetch",
			"requested_file", req.Filename,
			"shared_volume_path", fetcher.sharedVolumePath)
		otelUtils.SpanTrackEvent(ctx, "packageAlreadyExists", otelUtils.GetAttributesForPackage(pkg)...)
		return http.StatusOK, nil
	}

	tmpPath, err := utils.SanitizeFilePath(storePath+".tmp", fetcher.sharedVolumePath)
	if err != nil {
		logger.Error(err, "filename", req.Filename)
		return http.StatusBadRequest, fmt.Errorf("%s, request: %v", err, req)
	}

	if req.FetchType == fv1.FETCH_URL {
		otelUtils.SpanTrackEvent(ctx, "fetch_url", otelUtils.MapToAttributes(map[string]string{
			"package-name":      pkg.Name,
			"package-namespace": pkg.Namespace,
			"fetch-url":         req.URL,
		})...)
		// fetch the file and save it to the tmp path
		err := utils.DownloadUrl(ctx, fetcher.httpClient, req.URL, tmpPath)
		if err != nil {
			e := "failed to download url from fetch request"
			logger.Error(err, e, "url", req.URL)
			return http.StatusBadRequest, fmt.Errorf("%s: %s: %w", e, req.URL, err)
		}
	} else {
		var archive *fv1.Archive
		switch req.FetchType {
		case fv1.FETCH_SOURCE:
			archive = &pkg.Spec.Source
		case fv1.FETCH_DEPLOYMENT:
			// sometimes, the user may invoke the function even before the source code is built into a deploy pkg.
			// this results in executor sending a fetch request of type FETCH_DEPLOYMENT and since pkg.Spec.Deployment.Url will be empty,
			// we hit this "Get : unsupported protocol scheme "" error.
			// it may be useful to the user if we can send a more meaningful error in such a scenario.
			if pkg.Status.BuildStatus != fv1.BuildStatusSucceeded && pkg.Status.BuildStatus != fv1.BuildStatusNone {
				e := fmt.Errorf("cannot fetch deployment: package build status was not %q", fv1.BuildStatusSucceeded)
				logger.Error(e,
					"package_name", pkg.Name,
					"package_namespace", pkg.Namespace,
					"package_build_status", pkg.Status.BuildStatus)
				return http.StatusInternalServerError, fmt.Errorf("%s: pkg %s.%s has a status of %s", e, pkg.Name, pkg.Namespace, pkg.Status.BuildStatus)
			}
			archive = &pkg.Spec.Deployment
		default:
			return http.StatusBadRequest, fmt.Errorf("unknown fetch type: %v", req.FetchType)
		}

		// get package data as literal or by url
		if len(archive.Literal) > 0 {
			// write pkg.Literal into tmpPath
			err := os.WriteFile(tmpPath, archive.Literal, 0600)
			if err != nil {
				e := "failed to write file"
				logger.Error(err, e, "location", tmpPath)
				return http.StatusInternalServerError, fmt.Errorf("%s %s: %w", e, tmpPath, err)
			}
			otelUtils.SpanTrackEvent(ctx, "archiveLiteral", otelUtils.GetAttributesForPackage(pkg)...)
		} else {
			// download and verify
			otelUtils.SpanTrackEvent(ctx, "dowloadArchieveLiteral", otelUtils.MapToAttributes(map[string]string{
				"package-name":      pkg.Name,
				"package-namespace": pkg.Namespace,
				"archive-url":       archive.URL,
			})...)
			err := utils.DownloadUrl(ctx, fetcher.httpClient, archive.URL, tmpPath)
			if err != nil {
				e := "failed to download url from archive"
				logger.Error(err, e, "url", req.URL)
				return http.StatusBadRequest, fmt.Errorf("%s %s: %w", e, archive.URL, err)
			}

			// check file integrity only if checksum is not empty.
			if len(archive.Checksum.Sum) > 0 {
				checksum, err := utils.GetFileChecksum(tmpPath)
				if err != nil {
					e := "failed to get checksum"
					logger.Error(err, e)
					return http.StatusBadRequest, fmt.Errorf("%s: %w", e, err)
				}
				err = verifyChecksum(checksum, &archive.Checksum)
				if err != nil {
					e := "failed to verify checksum"
					logger.Error(err, e)
					return http.StatusBadRequest, fmt.Errorf("%s: %w", e, err)
				}
			}
		}
	}

	// checking if file is a zip
	if match, _ := utils.IsZip(ctx, tmpPath); match && !req.KeepArchive {
		// unarchive tmp file to a tmp unarchive path
		tmpUnarchivePath := filepath.Join(fetcher.sharedVolumePath, uuid.NewString())
		err := utils.Unarchive(ctx, tmpPath, tmpUnarchivePath)
		if err != nil {
			logger.Error(err, "error unarchive", "archive_location", tmpPath,
				"target_location", tmpUnarchivePath)
			return http.StatusInternalServerError, err
		}

		tmpPath = tmpUnarchivePath
	}

	// move tmp file to requested filename
	err = fetcher.rename(tmpPath, storePath)
	if err != nil {
		logger.Error(err, "error renaming file", "original_path", tmpPath,
			"rename_path", storePath)
		return http.StatusInternalServerError, fmt.Errorf("error renaming file: %w", err)
	}

	otelUtils.SpanTrackEvent(ctx, "packageFetched", otelUtils.GetAttributesForPackage(pkg)...)
	logger.Info("successfully placed", "location", storePath)
	return http.StatusOK, nil
}

// FetchSecretsAndCfgMaps fetches secrets and configmaps specified by user
// It returns the HTTP code and error if any
func (fetcher *Fetcher) FetchSecretsAndCfgMaps(ctx context.Context, secrets []fv1.SecretReference, cfgmaps []fv1.ConfigMapReference) (int, error) {
	logger := otelUtils.LoggerWithTraceID(ctx, fetcher.logger)

	if len(secrets) > 0 {
		for _, secret := range secrets {
			data, err := fetcher.kubeClient.CoreV1().Secrets(secret.Namespace).Get(ctx, secret.Name, metav1.GetOptions{})

			if err != nil {
				e := "error getting secret from kubeapi"

				httpCode := http.StatusInternalServerError
				if k8serr.IsNotFound(err) {
					httpCode = http.StatusNotFound
					e = "secret was not found in kubeapi"
				}
				logger.Error(err, e, "secret_name", secret.Name,
					"secret_namespace", secret.Namespace)

				return httpCode, errors.New(e)
			}

			secretDir, err := utils.SanitizeFilePath(filepath.Join(fetcher.sharedSecretPath, secret.Namespace, secret.Name), fetcher.sharedSecretPath)
			if err != nil {
				logger.Error(err, "directory", secretDir, "secret_name", secret.Name, "secret_namespace", secret.Namespace)
				return http.StatusBadRequest, fmt.Errorf("%s, request: %v", err, secret)
			}

			err = os.MkdirAll(secretDir, os.ModeDir|0750)
			if err != nil {
				e := "failed to create directory for secret"
				logger.Error(err, e, "directory", secretDir,
					"secret_name", secret.Name,
					"secret_namespace", secret.Namespace)
				return http.StatusInternalServerError, fmt.Errorf("%s: %s: %w", e, secretDir, err)
			}
			err = writeSecretOrConfigMap(data.Data, secretDir)
			if err != nil {
				logger.Error(err, "failed to write secret to file location", "location", secretDir,
					"secret_name", secret.Name,
					"secret_namespace", secret.Namespace)
				return http.StatusInternalServerError, err
			}
			otelUtils.SpanTrackEvent(ctx, "storedSecret", otelUtils.MapToAttributes(map[string]string{
				"secret-name":      secret.Name,
				"secret-namespace": secret.Namespace,
			})...)
		}
	}

	if len(cfgmaps) > 0 {
		for _, config := range cfgmaps {
			data, err := fetcher.kubeClient.CoreV1().ConfigMaps(config.Namespace).Get(ctx, config.Name, metav1.GetOptions{})

			if err != nil {
				e := "error getting configmap from kubeapi"

				httpCode := http.StatusInternalServerError
				if k8serr.IsNotFound(err) {
					httpCode = http.StatusNotFound
					e = "configmap was not found in kubeapi"
				}
				logger.Error(err, e, "config_map_name", config.Name,
					"config_map_namespace", config.Namespace)

				return httpCode, errors.New(e)
			}

			configDir, err := utils.SanitizeFilePath(filepath.Join(fetcher.sharedConfigPath, config.Namespace, config.Name), fetcher.sharedConfigPath)
			if err != nil {
				logger.Error(err, "directory", configDir, "config_map_name", config.Name, "config_map_namespace", config.Namespace)
				return http.StatusBadRequest, fmt.Errorf("%s, request: %v", err,
					config)
			}

			err = os.MkdirAll(configDir, os.ModeDir|0750)
			if err != nil {
				e := "failed to create directory for configmap"
				logger.Error(err, e, "directory", configDir,
					"config_map_name", config.Name,
					"config_map_namespace", config.Namespace)
				return http.StatusInternalServerError, fmt.Errorf("%s: %s: %w", e, configDir, err)
			}
			configMap := make(map[string][]byte)
			for key, val := range data.Data {
				configMap[key] = []byte(val)
			}
			err = writeSecretOrConfigMap(configMap, configDir)
			if err != nil {
				logger.Error(err, "failed to write configmap to file location", "location", configDir,
					"config_map_name", config.Name,
					"config_map_namespace", config.Namespace)
				return http.StatusInternalServerError, err
			}
			otelUtils.SpanTrackEvent(ctx, "storedConfigmap", otelUtils.MapToAttributes(map[string]string{
				"configmap-name":      config.Name,
				"configmap-namespace": config.Namespace,
			})...)
		}
	}

	return http.StatusOK, nil
}

func (fetcher *Fetcher) UploadHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := otelUtils.LoggerWithTraceID(ctx, fetcher.logger)

	if r.Method != "POST" {
		http.Error(w, "only POST is supported on this endpoint", http.StatusMethodNotAllowed)
		return
	}

	startTime := time.Now()
	defer func() {
		elapsed := time.Since(startTime)
		logger.Info("upload request done", "elapsed_time", elapsed)
	}()

	// parse request
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Error(err, "error reading request body")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var req ArchiveUploadRequest
	err = json.Unmarshal(body, &req)
	if err != nil {
		logger.Error(err, "error parsing request body")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	logger.Info("fetcher received upload request", "request", req)

	srcFilepath, err := utils.SanitizeFilePath(filepath.Join(fetcher.sharedVolumePath, req.Filename), fetcher.sharedVolumePath)
	if err != nil {
		logger.Error(err, "error sanitizing file path")
		http.Error(w, fmt.Sprintf("%s: %v", err, req.Filename), http.StatusBadRequest)
		return
	}
	dstFilepath, err := utils.SanitizeFilePath(filepath.Join(fetcher.sharedVolumePath, req.Filename+".zip"), fetcher.sharedVolumePath)
	if err != nil {
		logger.Error(err, "error sanitizing file path")
		http.Error(w, fmt.Sprintf("%s: %v", err, req.Filename), http.StatusBadRequest)
		return
	}

	defer func() {
		errC := utils.DeleteOldPackages(srcFilepath, "DEPLOY_PKG")
		if errC != nil {
			m := "error deleting deploy package after upload"
			logger.Error(errC, m)
		}
	}()

	if req.ArchivePackage {
		err = utils.Archive(ctx, srcFilepath, dstFilepath)
		if err != nil {
			e := "error archiving zip file"
			logger.Error(err, e, "source", srcFilepath, "destination", dstFilepath)
			http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
			return
		}
	} else {
		err = os.Rename(srcFilepath, dstFilepath)
		if err != nil {
			e := "error renaming the archive"
			logger.Error(err, e, "source", srcFilepath, "destination", dstFilepath)
			http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
			return
		}
	}

	logger.Info("starting upload...")
	ssClient := storageSvcClient.MakeClient(req.StorageSvcUrl)

	fileID, err := ssClient.Upload(ctx, dstFilepath, nil)
	if err != nil {
		e := "error uploading zip file"
		logger.Error(err, e, "file", dstFilepath)
		http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
		return
	}

	sum, err := utils.GetFileChecksum(dstFilepath)
	if err != nil {
		e := "error calculating checksum of zip file"
		logger.Error(err, e, "file", dstFilepath)
		http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
		return
	}

	resp := ArchiveUploadResponse{
		ArchiveDownloadUrl: ssClient.GetUrl(fileID),
		Checksum:           *sum,
	}

	rBody, err := json.Marshal(resp)
	if err != nil {
		e := "error encoding upload response"
		logger.Error(err, e)
		http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
		return
	}

	logger.Info("completed upload request")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(rBody)
	if err != nil {
		e := "error writing response"
		logger.Error(err, e)
		http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
	}

}

func (fetcher *Fetcher) rename(src string, dst string) error {
	err := os.Rename(src, dst)
	if err != nil {
		return fmt.Errorf("failed to move file: %w", err)
	}
	return nil
}

// getPkgInformation gets package information from k8s api server.
func (fetcher *Fetcher) getPkgInformation(ctx context.Context, req FunctionFetchRequest) (pkg *fv1.Package, err error) {
	logger := otelUtils.LoggerWithTraceID(ctx, fetcher.logger)
	maxRetries := 5
	for i := range maxRetries {
		otelUtils.SpanTrackEvent(ctx, "fetchPkgInfo", otelUtils.MapToAttributes(map[string]string{
			"package_name":      req.Package.Name,
			"package_namespace": req.Package.Namespace,
			"retry_count":       strconv.Itoa(i),
		})...)
		// TODO: pass resource version in the GetOptions, added warning for now
		pkg, err = fetcher.fissionClient.CoreV1().Packages(req.Package.Namespace).Get(ctx, req.Package.Name, metav1.GetOptions{})
		if err == nil {
			if req.Package.ResourceVersion != pkg.ResourceVersion {
				logger.Info("package resource version mismatch", "pkgName", req.Package.Name, "pkgNamespace", req.Package.Namespace, "pkgResourceVersion", req.Package.ResourceVersion, "fetchedResourceVersion", pkg.ResourceVersion)
			}
			return pkg, nil
		}
		if i < maxRetries-1 {
			// In some cases, creating a package and querying the package info
			// immediately, the Kubernetes API server will return "not found"
			// error. So retry the query again after some time.

			if k8serr.IsNotFound(err) {
				time.Sleep(50 * time.Duration(i+1) * time.Millisecond)
				continue
			}

			// All outbound requests are blocked if istio is enabled at the first seconds.
			// So if an error is a "connection refused" or "dial" error, wait for a while
			// before retrying so that envoy proxy will start to serve requests.
			// For details, see https://github.com/istio/istio/issues/12187
			netErr := network.Adapter(err)
			if netErr != nil && (netErr.IsDialError() || netErr.IsConnRefusedError()) {
				time.Sleep(500 * time.Duration(i+1) * time.Millisecond)
			}
		}
	}
	return nil, err
}

func (fetcher *Fetcher) SpecializePod(ctx context.Context, fetchReq FunctionFetchRequest, loadReq FunctionLoadRequest) (int, error) {
	logger := otelUtils.LoggerWithTraceID(ctx, fetcher.logger)
	startTime := time.Now()
	defer func() {
		elapsed := time.Since(startTime)
		logger.Info("specialize request done", "elapsed_time", elapsed)
	}()

	pkg, err := fetcher.getPkgInformation(ctx, fetchReq)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("error getting package information: %w", err)
	}

	code, err := fetcher.Fetch(ctx, pkg, fetchReq)
	if err != nil {
		return code, fmt.Errorf("error fetching deploy package: %w", err)
	}

	code, err = fetcher.FetchSecretsAndCfgMaps(ctx, fetchReq.Secrets, fetchReq.ConfigMaps)
	if err != nil {
		return code, fmt.Errorf("error fetching secrets/configs: %w", err)
	}

	// Specialize the pod

	maxRetries := 30
	var contentType string
	var specializeURL string
	var reader *bytes.Reader

	loadPayload, err := json.Marshal(loadReq)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("error encoding load request: %w", err)
	}

	// Instead of using "localhost", here we use "127.0.0.1" for
	// inter-pod communication to prevent wrongly record returned from DNS.

	if loadReq.EnvVersion >= 2 {
		contentType = "application/json"
		specializeURL = "http://127.0.0.1:8888/v2/specialize"
		reader = bytes.NewReader(loadPayload)
		logger.Info("calling environment v2 specialization endpoint")
	} else {
		contentType = "text/plain"
		specializeURL = "http://127.0.0.1:8888/specialize"
		reader = bytes.NewReader([]byte{})
		logger.Info("calling environment v1 specialization endpoint")
	}

	for i := range maxRetries {
		otelUtils.SpanTrackEvent(ctx, "specializeCall", otelUtils.MapToAttributes(map[string]string{
			"url": specializeURL,
		})...)
		resp, err := ctxhttp.Post(ctx, fetcher.httpClient, specializeURL, contentType, reader)
		if err == nil && resp.StatusCode < 300 {
			// Success
			resp.Body.Close()
			return resp.StatusCode, nil
		}

		netErr := network.Adapter(err)
		// Only retry for the specific case of a connection error.
		if netErr != nil && (netErr.IsConnRefusedError() || netErr.IsDialError()) {
			if i < maxRetries-1 {
				time.Sleep(500 * time.Duration(2*i) * time.Millisecond)
				logger.Error(netErr, "error connecting to function environment pod for specialization request, retrying")
				continue
			}
		}

		// for 4xx, 5xx
		if err == nil {
			err = ferror.MakeErrorFromHTTP(resp)
		}

		statusCode := http.StatusInternalServerError
		if resp != nil {
			statusCode = resp.StatusCode
		}
		return statusCode, fmt.Errorf("error specializing function pod: %w", err)
	}

	return http.StatusInternalServerError, fmt.Errorf("error specializing function pod after %v times: %w", maxRetries, err)
}

// WsStartHandler is used to generate websocket events in Kubernetes
func (fetcher *Fetcher) WsStartHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := otelUtils.LoggerWithTraceID(ctx, fetcher.logger)
	if r.Method != "GET" {
		http.Error(w, "only GET is supported on this endpoint", http.StatusMethodNotAllowed)
		return
	}
	rec, err := eventRecorder(fetcher.kubeClient, logger)
	if err != nil {
		logger.Error(err, "Error creating recorder")
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	pod, err := fetcher.kubeClient.CoreV1().Pods(fetcher.Info.Namespace).Get(ctx, fetcher.Info.Name, metav1.GetOptions{})
	if err != nil {
		logger.Error(err, "Failed to get the pod")
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	ref, err := reference.GetReference(scheme.Scheme, pod)
	if err != nil {
		logger.Error(err, "Could not get reference for pod")
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	rec.Event(ref, corev1.EventTypeNormal, "WsConnectionStarted", "Websocket connection has been formed on this pod")
	logger.Info("Sent websocket initiation event")

	w.WriteHeader(http.StatusOK)
}

// WsEndHandler is used to generate inactive events in Kubernetes
func (fetcher *Fetcher) WsEndHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	logger := otelUtils.LoggerWithTraceID(ctx, fetcher.logger)

	if r.Method != "GET" {
		http.Error(w, "only GET is supported on this endpoint", http.StatusMethodNotAllowed)
		return
	}
	rec, err := eventRecorder(fetcher.kubeClient, logger)
	if err != nil {
		logger.Error(err, "Error creating recorder")
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	pod, err := fetcher.kubeClient.CoreV1().Pods(fetcher.Info.Namespace).Get(ctx, fetcher.Info.Name, metav1.GetOptions{})
	if err != nil {
		logger.Error(err, "Failed to get the pod")
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	// There will only be one time since we've used field selector
	ref, err := reference.GetReference(scheme.Scheme, pod)
	if err != nil {
		logger.Error(err, "Could not get reference for pod")
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	// We could use Eventf and supply the amount of time the connection was inactive although, in case of multiple connections, it doesn't make sense
	rec.Event(ref, corev1.EventTypeNormal, "NoActiveConnections", "Connection has been inactive")
	logger.Info("Sent no active connections event")

	w.WriteHeader(http.StatusOK)
}

func eventRecorder(kubeClient kubernetes.Interface, logger logr.Logger) (record.EventRecorder, error) {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(logger.Info)
	eventBroadcaster.StartRecordingToSink(
		&typedcorev1.EventSinkImpl{
			Interface: kubeClient.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(
		scheme.Scheme,
		corev1.EventSource{Component: "fetcher"})
	return recorder, nil
}
