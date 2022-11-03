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
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mholt/archiver/v3"
	"github.com/pkg/errors"
	uuid "github.com/satori/go.uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
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
		logger           *zap.Logger
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

func MakeFetcher(logger *zap.Logger, sharedVolumePath string, sharedSecretPath string, sharedConfigPath string) (*Fetcher, error) {
	fLogger := logger.Named("fetcher")
	err := makeVolumeDir(sharedVolumePath)
	if err != nil {
		fLogger.Fatal("error creating shared volume directory", zap.Error(err), zap.String("directory", sharedVolumePath))
	}
	err = makeVolumeDir(sharedSecretPath)
	if err != nil {
		fLogger.Fatal("error creating shared secret directory", zap.Error(err), zap.String("directory", sharedSecretPath))
	}
	err = makeVolumeDir(sharedConfigPath)
	if err != nil {
		fLogger.Fatal("error creating shared config directory", zap.Error(err), zap.String("directory", sharedConfigPath))
	}

	fissionClient, kubeClient, _, _, err := crd.MakeFissionClient()
	if err != nil {
		return nil, errors.Wrap(err, "error making the fission / kube client")
	}

	name, err := os.ReadFile(fv1.PodInfoMount + "/name")
	if err != nil {
		return nil, errors.Wrap(err, "error reading pod name from downward volume")
	}

	namespace, err := os.ReadFile(fv1.PodInfoMount + "/namespace")
	if err != nil {
		return nil, errors.Wrap(err, "error reading pod namespace from downward volume")
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
			return errors.Wrapf(err, "Failed to write file %s", writeFilePath)
		}
	}
	return nil
}

func (fetcher *Fetcher) VersionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, err := w.Write([]byte(info.BuildInfo().String()))
	if err != nil {
		fetcher.logger.Error("error writing response", zap.Error(err))
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
		logger.Info("fetch request done", zap.Duration("elapsed_time", elapsed))
	}()

	// parse request
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Error("error reading request body", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var req FunctionFetchRequest
	err = json.Unmarshal(body, &req)
	if err != nil {
		logger.Error("error parsing request body", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	pkg, err := fetcher.getPkgInformation(ctx, req)
	if err != nil {
		logger.Error("error getting package information", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	code, err := fetcher.Fetch(ctx, pkg, req)
	if err != nil {
		logger.Error("error fetching", zap.Error(err))
		http.Error(w, err.Error(), code)
		return
	}

	logger.Info("checking secrets/cfgmaps")
	code, err = fetcher.FetchSecretsAndCfgMaps(ctx, req.Secrets, req.ConfigMaps)
	if err != nil {
		logger.Error("error fetching secrets and config maps", zap.Error(err))
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
		logger.Error("error reading request body", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var req FunctionSpecializeRequest
	err = json.Unmarshal(body, &req)
	if err != nil {
		logger.Error("error parsing request body", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err = fetcher.SpecializePod(ctx, req.FetchReq, req.LoadReq)
	if err != nil {
		logger.Error("error specializing pod", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// all done
	w.WriteHeader(http.StatusOK)
}

// Fetch takes FetchRequest and makes the fetch call
// It returns the HTTP code and error if any
func (fetcher *Fetcher) Fetch(ctx context.Context, pkg *fv1.Package, req FunctionFetchRequest) (int, error) {
	logger := otelUtils.LoggerWithTraceID(ctx, fetcher.logger)

	// check that the requested filename is not an empty string and error out if so
	if len(req.Filename) == 0 {
		e := "fetch request received for an empty file name"
		logger.Error(e, zap.Any("request", req))
		return http.StatusBadRequest, errors.New(fmt.Sprintf("%s, request: %v", e, req))
	}

	// verify first if the file already exists.
	if _, err := os.Stat(filepath.Join(fetcher.sharedVolumePath, req.Filename)); err == nil {
		logger.Info("requested file already exists at shared volume - skipping fetch",
			zap.String("requested_file", req.Filename),
			zap.String("shared_volume_path", fetcher.sharedVolumePath))
		otelUtils.SpanTrackEvent(ctx, "packageAlreadyExists", otelUtils.GetAttributesForPackage(pkg)...)
		return http.StatusOK, nil
	}

	tmpFile := req.Filename + ".tmp"
	tmpPath := filepath.Join(fetcher.sharedVolumePath, tmpFile)

	if req.FetchType == fv1.FETCH_URL {
		otelUtils.SpanTrackEvent(ctx, "fetch_url", otelUtils.MapToAttributes(map[string]string{
			"package-name":      pkg.Name,
			"package-namespace": pkg.Namespace,
			"fetch-url":         req.Url,
		})...)
		// fetch the file and save it to the tmp path
		err := utils.DownloadUrl(ctx, fetcher.httpClient, req.Url, tmpPath)
		if err != nil {
			e := "failed to download url"
			logger.Error(e, zap.Error(err), zap.String("url", req.Url))
			return http.StatusBadRequest, errors.Wrapf(err, "%s: %s", e, req.Url)
		}
	} else {
		var archive *fv1.Archive
		if req.FetchType == fv1.FETCH_SOURCE {
			archive = &pkg.Spec.Source
		} else if req.FetchType == fv1.FETCH_DEPLOYMENT {
			// sometimes, the user may invoke the function even before the source code is built into a deploy pkg.
			// this results in executor sending a fetch request of type FETCH_DEPLOYMENT and since pkg.Spec.Deployment.Url will be empty,
			// we hit this "Get : unsupported protocol scheme "" error.
			// it may be useful to the user if we can send a more meaningful error in such a scenario.
			if pkg.Status.BuildStatus != fv1.BuildStatusSucceeded && pkg.Status.BuildStatus != fv1.BuildStatusNone {
				e := fmt.Sprintf("cannot fetch deployment: package build status was not %q", fv1.BuildStatusSucceeded)
				logger.Error(e,
					zap.String("package_name", pkg.ObjectMeta.Name),
					zap.String("package_namespace", pkg.ObjectMeta.Namespace),
					zap.Any("package_build_status", pkg.Status.BuildStatus))
				return http.StatusInternalServerError, errors.New(fmt.Sprintf("%s: pkg %s.%s has a status of %s", e, pkg.ObjectMeta.Name, pkg.ObjectMeta.Namespace, pkg.Status.BuildStatus))
			}
			archive = &pkg.Spec.Deployment
		} else {
			return http.StatusBadRequest, fmt.Errorf("unknown fetch type: %v", req.FetchType)
		}

		// get package data as literal or by url
		if len(archive.Literal) > 0 {
			// write pkg.Literal into tmpPath
			err := os.WriteFile(tmpPath, archive.Literal, 0600)
			if err != nil {
				e := "failed to write file"
				logger.Error(e, zap.Error(err), zap.String("location", tmpPath))
				return http.StatusInternalServerError, errors.Wrapf(err, "%s %s", e, tmpPath)
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
				e := "failed to download url"
				logger.Error(e, zap.Error(err), zap.String("url", req.Url))
				return http.StatusBadRequest, errors.Wrapf(err, "%s %s", e, req.Url)
			}

			// check file integrity only if checksum is not empty.
			if len(archive.Checksum.Sum) > 0 {
				checksum, err := utils.GetFileChecksum(tmpPath)
				if err != nil {
					e := "failed to get checksum"
					logger.Error(e, zap.Error(err))
					return http.StatusBadRequest, errors.Wrap(err, e)
				}
				err = verifyChecksum(checksum, &archive.Checksum)
				if err != nil {
					e := "failed to verify checksum"
					logger.Error(e, zap.Error(err))
					return http.StatusBadRequest, errors.Wrap(err, e)
				}
			}
		}
	}

	//checking if file is a zip
	if match, _ := utils.IsZip(tmpPath); match && !req.KeepArchive {
		// unarchive tmp file to a tmp unarchive path
		id, err := uuid.NewV4()
		if err != nil {
			logger.Error("error generating uuid",
				zap.Error(err),
				zap.String("archive_location", tmpPath))
			return http.StatusInternalServerError, err
		}

		tmpUnarchivePath := filepath.Join(fetcher.sharedVolumePath, id.String())
		err = fetcher.unarchive(tmpPath, tmpUnarchivePath)
		if err != nil {
			logger.Error("error unarchive",
				zap.Error(err),
				zap.String("archive_location", tmpPath),
				zap.String("target_location", tmpUnarchivePath))
			return http.StatusInternalServerError, err
		}

		tmpPath = tmpUnarchivePath
	}

	// move tmp file to requested filename
	renamePath := filepath.Join(fetcher.sharedVolumePath, req.Filename)
	err := fetcher.rename(tmpPath, renamePath)
	if err != nil {
		logger.Error("error renaming file",
			zap.Error(err),
			zap.String("original_path", tmpPath),
			zap.String("rename_path", renamePath))
		return http.StatusInternalServerError, err
	}

	otelUtils.SpanTrackEvent(ctx, "packageFetched", otelUtils.GetAttributesForPackage(pkg)...)
	logger.Info("successfully placed", zap.String("location", renamePath))
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
				logger.Error(e,
					zap.Error(err),
					zap.String("secret_name", secret.Name),
					zap.String("secret_namespace", secret.Namespace))

				return httpCode, errors.New(e)
			}

			secretPath := filepath.Join(secret.Namespace, secret.Name)
			secretDir := filepath.Join(fetcher.sharedSecretPath, secretPath)
			err = os.MkdirAll(secretDir, os.ModeDir|0750)
			if err != nil {
				e := "failed to create directory for secret"
				logger.Error(e,
					zap.Error(err),
					zap.String("directory", secretDir),
					zap.String("secret_name", secret.Name),
					zap.String("secret_namespace", secret.Namespace))
				return http.StatusInternalServerError, errors.Wrapf(err, "%s: %s", e, secretDir)
			}
			err = writeSecretOrConfigMap(data.Data, secretDir)
			if err != nil {
				logger.Error("failed to write secret to file location",
					zap.Error(err),
					zap.String("location", secretDir),
					zap.String("secret_name", secret.Name),
					zap.String("secret_namespace", secret.Namespace))
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
				logger.Error(e,
					zap.Error(err),
					zap.String("config_map_name", config.Name),
					zap.String("config_map_namespace", config.Namespace))

				return httpCode, errors.New(e)
			}

			configPath := filepath.Join(config.Namespace, config.Name)
			configDir := filepath.Join(fetcher.sharedConfigPath, configPath)
			err = os.MkdirAll(configDir, os.ModeDir|0750)
			if err != nil {
				e := "failed to create directory for configmap"
				logger.Error(e,
					zap.Error(err),
					zap.String("directory", configDir),
					zap.String("config_map_name", config.Name),
					zap.String("config_map_namespace", config.Namespace))
				return http.StatusInternalServerError, errors.Wrapf(err, "%s: %s", e, configDir)
			}
			configMap := make(map[string][]byte)
			for key, val := range data.Data {
				configMap[key] = []byte(val)
			}
			err = writeSecretOrConfigMap(configMap, configDir)
			if err != nil {
				logger.Error("failed to write configmap to file location",
					zap.Error(err),
					zap.String("location", configDir),
					zap.String("config_map_name", config.Name),
					zap.String("config_map_namespace", config.Namespace))
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
		logger.Info("upload request done", zap.Duration("elapsed_time", elapsed))
	}()

	// parse request
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Error("error reading request body", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var req ArchiveUploadRequest
	err = json.Unmarshal(body, &req)
	if err != nil {
		logger.Error("error parsing request body", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	logger.Info("fetcher received upload request", zap.Any("request", req))

	zipFilename := req.Filename + ".zip"
	srcFilepath := filepath.Join(fetcher.sharedVolumePath, req.Filename)
	dstFilepath := filepath.Join(fetcher.sharedVolumePath, zipFilename)

	if req.ArchivePackage {
		err = fetcher.archive(srcFilepath, dstFilepath)
		if err != nil {
			e := "error archiving zip file"
			logger.Error(e, zap.Error(err), zap.String("source", srcFilepath), zap.String("destination", dstFilepath))
			http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
			return
		}
	} else {
		err = os.Rename(srcFilepath, dstFilepath)
		if err != nil {
			e := "error renaming the archive"
			logger.Error(e, zap.Error(err), zap.String("source", srcFilepath), zap.String("destination", dstFilepath))
			http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
			return
		}
	}

	logger.Info("starting upload...")
	ssClient := storageSvcClient.MakeClient(req.StorageSvcUrl)

	fileID, err := ssClient.Upload(ctx, dstFilepath, nil)
	if err != nil {
		e := "error uploading zip file"
		logger.Error(e, zap.Error(err), zap.String("file", dstFilepath))
		http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
		return
	}

	sum, err := utils.GetFileChecksum(dstFilepath)
	if err != nil {
		e := "error calculating checksum of zip file"
		logger.Error(e, zap.Error(err), zap.String("file", dstFilepath))
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
		logger.Error(e, zap.Error(err))
		http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
		return
	}

	logger.Info("completed upload request")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(rBody)
	if err != nil {
		e := "error writing response"
		logger.Error(e, zap.Error(err))
		http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
	}
}

func (fetcher *Fetcher) rename(src string, dst string) error {
	err := os.Rename(src, dst)
	if err != nil {
		return errors.Wrap(err, "failed to move file")
	}
	return nil
}

// archive zips the contents of directory at src into a new zip file
// at dst (note that the contents are zipped, not the directory itself).
func (fetcher *Fetcher) archive(src string, dst string) error {
	var files []string
	target, err := os.Stat(src)
	if err != nil {
		return errors.Wrap(err, "failed to zip file")
	}
	if target.IsDir() {
		// list all
		fs, _ := os.ReadDir(src)
		for _, f := range fs {
			files = append(files, filepath.Join(src, f.Name()))
		}
	} else {
		files = append(files, src)
	}
	return archiver.DefaultZip.Archive(files, dst)
}

// unarchive is a function that unzips a zip file to destination
func (fetcher *Fetcher) unarchive(src string, dst string) error {
	err := archiver.DefaultZip.Unarchive(src, dst)
	if err != nil {
		return fmt.Errorf("failed to unzip file: %w", err)
	}
	return nil
}

// getPkgInformation gets package information from k8s api server.
func (fetcher *Fetcher) getPkgInformation(ctx context.Context, req FunctionFetchRequest) (pkg *fv1.Package, err error) {
	logger := otelUtils.LoggerWithTraceID(ctx, fetcher.logger)
	maxRetries := 5
	for i := 0; i < maxRetries; i++ {
		otelUtils.SpanTrackEvent(ctx, "fetchPkgInfo", otelUtils.MapToAttributes(map[string]string{
			"package_name":      req.Package.Name,
			"package_namespace": req.Package.Namespace,
			"retry_count":       strconv.Itoa(i),
		})...)
		// TODO: pass resource version in the GetOptions, added warning for now
		pkg, err = fetcher.fissionClient.CoreV1().Packages(req.Package.Namespace).Get(ctx, req.Package.Name, metav1.GetOptions{})
		if err == nil {
			if req.Package.ResourceVersion != pkg.ResourceVersion {
				logger.Warn("package resource version mismatch", zap.String("pkgName", req.Package.Name), zap.String("pkgNamespace", req.Package.Namespace), zap.String("pkgResourceVersion", req.Package.ResourceVersion), zap.String("fetchedResourceVersion", pkg.ResourceVersion))
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

func (fetcher *Fetcher) SpecializePod(ctx context.Context, fetchReq FunctionFetchRequest, loadReq FunctionLoadRequest) error {
	logger := otelUtils.LoggerWithTraceID(ctx, fetcher.logger)
	startTime := time.Now()
	defer func() {
		elapsed := time.Since(startTime)
		logger.Info("specialize request done", zap.Duration("elapsed_time", elapsed))
	}()

	pkg, err := fetcher.getPkgInformation(ctx, fetchReq)
	if err != nil {
		return errors.Wrap(err, "error getting package information")
	}

	_, err = fetcher.Fetch(ctx, pkg, fetchReq)
	if err != nil {
		return errors.Wrap(err, "error fetching deploy package")
	}

	_, err = fetcher.FetchSecretsAndCfgMaps(ctx, fetchReq.Secrets, fetchReq.ConfigMaps)
	if err != nil {
		return errors.Wrap(err, "error fetching secrets/configs")
	}

	// Specialize the pod

	maxRetries := 30
	var contentType string
	var specializeURL string
	var reader *bytes.Reader

	loadPayload, err := json.Marshal(loadReq)
	if err != nil {
		return errors.Wrap(err, "error encoding load request")
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

	for i := 0; i < maxRetries; i++ {
		otelUtils.SpanTrackEvent(ctx, "specializeCall", otelUtils.MapToAttributes(map[string]string{
			"url": specializeURL,
		})...)
		resp, err := ctxhttp.Post(ctx, fetcher.httpClient, specializeURL, contentType, reader)
		if err == nil && resp.StatusCode < 300 {
			// Success
			resp.Body.Close()
			return nil
		}

		netErr := network.Adapter(err)
		// Only retry for the specific case of a connection error.
		if netErr != nil && (netErr.IsConnRefusedError() || netErr.IsDialError()) {
			if i < maxRetries-1 {
				time.Sleep(500 * time.Duration(2*i) * time.Millisecond)
				logger.Error("error connecting to function environment pod for specialization request, retrying", zap.Error(netErr))
				continue
			}
		}

		// for 4xx, 5xx
		if err == nil {
			err = ferror.MakeErrorFromHTTP(resp)
		}

		return errors.Wrap(err, "error specializing function pod")
	}

	return errors.Wrapf(err, "error specializing function pod after %v times", maxRetries)
}

// WsStartHandler is used to generate websocket events in Kubernetes
func (fetcher *Fetcher) WsStartHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := otelUtils.LoggerWithTraceID(ctx, fetcher.logger)
	if r.Method != "GET" {
		http.Error(w, "only GET is supported on this endpoint", http.StatusMethodNotAllowed)
		return
	}
	rec, err := eventRecorder(fetcher.kubeClient)
	if err != nil {
		logger.Error("Error creating recorder", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	pods, err := fetcher.kubeClient.CoreV1().Pods(fetcher.Info.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + fetcher.Info.Name,
	})
	if err != nil {
		logger.Error("Failed to get the pod", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	for _, pod := range pods.Items {
		ref, err := reference.GetReference(scheme.Scheme, &pod)
		if err != nil {
			logger.Error("Could not get reference for pod", zap.Error(err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		rec.Event(ref, corev1.EventTypeNormal, "WsConnectionStarted", "Websocket connection has been formed on this pod")
		logger.Info("Sent websocket initiation event")
	}
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
	rec, err := eventRecorder(fetcher.kubeClient)
	if err != nil {
		logger.Error("Error creating recorder", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	pods, err := fetcher.kubeClient.CoreV1().Pods(fetcher.Info.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + fetcher.Info.Name,
	})
	if err != nil {
		logger.Error("Failed to get the pod", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
	for _, pod := range pods.Items {
		// There will only be one time since we've used field selector
		ref, err := reference.GetReference(scheme.Scheme, &pod)
		if err != nil {
			logger.Error("Could not get reference for pod", zap.Error(err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		// We could use Eventf and supply the amount of time the connection was inactive although, in case of multiple connections, it doesn't make sense
		rec.Event(ref, corev1.EventTypeNormal, "NoActiveConnections", "Connection has been inactive")
		logger.Info("Sent no active connections event")
	}
	w.WriteHeader(http.StatusOK)
}

func eventRecorder(kubeClient kubernetes.Interface) (record.EventRecorder, error) {
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(zap.S().Infof)
	eventBroadcaster.StartRecordingToSink(
		&typedcorev1.EventSinkImpl{
			Interface: kubeClient.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(
		scheme.Scheme,
		corev1.EventSource{Component: "fetcher"})
	return recorder, nil
}
