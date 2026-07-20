// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fetcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/crd"
	ferror "github.com/fission/fission/pkg/error"
	"github.com/fission/fission/pkg/error/network"
	"github.com/fission/fission/pkg/generated/clientset/versioned"
	"github.com/fission/fission/pkg/info"
	storageSvcClient "github.com/fission/fission/pkg/storagesvc/client"
	"github.com/fission/fission/pkg/svcinfo"
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
		// storageHTTPClient is used for storagesvc archive downloads
		// only. It carries the HMAC signer when internalAuth is enabled
		// so requests to /v1/archive carry a valid X-Fission-Auth-Signature.
		// httpClient stays unsigned because it also serves the user
		// container's /v2/specialize endpoint, which is not the auth
		// boundary and would otherwise reject the extra headers (or be
		// confused by the body buffering that signing requires).
		storageHTTPClient *http.Client
		Info              PodInfo
	}
	PodInfo struct {
		Name      string
		Namespace string
	}
)

func makeVolumeDir(dirPath string) error {
	return os.MkdirAll(dirPath, os.ModeDir|0750)
}

// namespaceHeaderRoundTripper sets the X-Fission-Auth-Namespace header on every
// outgoing request, so a namespace-scoped storagesvc verifier derives this pod's
// per-namespace key. The header is not signed (it does not need to be — the key
// is self-protecting), so it can be set outside the signer wrapper.
type namespaceHeaderRoundTripper struct {
	namespace string
	next      http.RoundTripper
}

func (n *namespaceHeaderRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(hmacauth.HeaderNamespace, n.namespace)
	return n.next.RoundTrip(r)
}

// storageSigningTransport wraps base with the right storagesvc request signer for
// this fetcher pod: a derived per-namespace key + namespace header when the tenant
// controller mounted one (FISSION_STORAGE_KEY — the pod never holds the master),
// else the master-derived ServiceStoragesvc signer, else (no auth) base unchanged.
// Centralised so every storagesvc client the fetcher builds signs identically.
func storageSigningTransport(base http.RoundTripper, namespace string) http.RoundTripper {
	if storageKey := hmacauth.DecodeKeyFromEnv(os.Getenv("FISSION_STORAGE_KEY")); len(storageKey) > 0 {
		return &namespaceHeaderRoundTripper{namespace: namespace, next: hmacauth.NewSigner(storageKey, base, time.Now)}
	}
	if secret := storageSvcClient.HMACSecretFromEnv(); len(secret) > 0 {
		return hmacauth.ServiceSigner(secret, hmacauth.ServiceStoragesvc, base, time.Now)
	}
	return base
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
	// storageHTTPClient signs requests to storagesvc with the HMAC
	// scheme described in docs/internal-auth/00-design.md when the
	// chart's internal-auth master secret is mounted. The signer uses
	// the per-service derived key for ServiceStoragesvc; sharing the
	// persistent httpClient with the signer wrapper would also sign
	// the /v2/specialize POST sent to the user container, which
	// doesn't expect our auth headers — keep the two clients separate.
	storageHC := &http.Client{Transport: storageSigningTransport(otelhttp.NewTransport(http.DefaultTransport), string(namespace))}
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
		httpClient:        hc,
		storageHTTPClient: storageHC,
	}, nil
}

// httpClientForURL picks the right downloader for a URL: the signed
// storageHTTPClient when the URL targets storagesvc (path prefix
// /v1/archive), the unsigned httpClient otherwise. Without this guard
// the fetcher would attach our internal HMAC headers to S3 archive
// downloads, FETCH_URL targets the user supplied (potentially public
// HTTP servers), and any other external endpoint — leaking auth
// metadata to third parties for no benefit.
//
// Storagesvc URLs always live at /v1/archive[?id=...]; that path is
// distinctive enough for a host-agnostic match (the host depends on
// service name, namespace, port-forward, etc., so prefix-matching the
// host is fragile). A URL whose path doesn't start with /v1/archive
// is by definition not storagesvc and gets the unsigned client.
//
// On parse failure we default to the unsigned client — sending the
// signed headers to an unparseable URL is the worse failure mode (data
// exfiltration via headers); failing the download with a clearer error
// from the inner transport is fine.
func (fetcher *Fetcher) httpClientForURL(rawURL string) *http.Client {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fetcher.httpClient
	}
	if strings.HasPrefix(parsed.Path, "/v1/archive") {
		return fetcher.storageHTTPClient
	}
	return fetcher.httpClient
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
	// Open the os.Root once and write every key through it: os.Root confines
	// each write to dirPath (the keys are Secret/ConfigMap data keys), and
	// reusing one root avoids an openat per key.
	root, err := os.OpenRoot(dirPath)
	if err != nil {
		return fmt.Errorf("failed to open directory %s: %w", dirPath, err)
	}
	defer root.Close()
	for key, val := range dataMap {
		if err := root.WriteFile(key, val, 0750); err != nil {
			return fmt.Errorf("failed to write file %s in %s: %w", key, dirPath, err)
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

	storePath, err := utils.RootJoin(fetcher.sharedVolumePath, req.Filename)
	if err != nil {
		logger.Error(err, "filename", req.Filename)
		return http.StatusBadRequest, fmt.Errorf("%w, request: %v", err, req)
	}

	// verify first if the file already exists.
	if _, err := utils.RootStat(fetcher.sharedVolumePath, storePath); err == nil {
		logger.Info("requested file already exists at shared volume - skipping fetch",
			"requested_file", req.Filename,
			"shared_volume_path", fetcher.sharedVolumePath)
		otelUtils.SpanTrackEvent(ctx, "packageAlreadyExists", otelUtils.GetAttributesForPackage(pkg)...)
		return http.StatusOK, nil
	}

	tmpPath, err := utils.RootJoin(fetcher.sharedVolumePath, storePath+".tmp")
	if err != nil {
		logger.Error(err, "filename", req.Filename)
		return http.StatusBadRequest, fmt.Errorf("%w, request: %v", err, req)
	}

	if req.FetchType == fv1.FETCH_URL {
		otelUtils.SpanTrackEvent(ctx, "fetch_url", otelUtils.MapToAttributes(map[string]string{
			"package-name":      pkg.Name,
			"package-namespace": pkg.Namespace,
			"fetch-url":         req.URL,
		})...)
		// fetch the file and save it to the tmp path. FETCH_URL targets
		// are user-supplied — pick the unsigned client unless the URL
		// happens to point at our own storagesvc.
		err := utils.DownloadUrlToRoot(ctx, fetcher.httpClientForURL(req.URL), req.URL, fetcher.sharedVolumePath, tmpPath)
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

		// OCI archives (RFC-0001) have their own pull/extract path; the
		// literal/url/zip handling below is tarball-specific.
		if archive.OCI != nil {
			return fetcher.fetchOCI(ctx, pkg, archive.OCI, storePath)
		}

		// get package data as literal or by url
		if len(archive.Literal) > 0 {
			// write pkg.Literal into tmpPath
			err := utils.RootWriteFile(fetcher.sharedVolumePath, tmpPath, archive.Literal, 0600)
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
			// archive.URL may resolve to storagesvc (/v1/archive?id=...)
			// or an external storage backend (S3, GCS, etc.). Sign only
			// when the URL targets storagesvc.
			err := utils.DownloadUrlToRoot(ctx, fetcher.httpClientForURL(archive.URL), archive.URL, fetcher.sharedVolumePath, tmpPath)
			if err != nil {
				e := "failed to download url from archive"
				logger.Error(err, e, "url", req.URL)
				return http.StatusBadRequest, fmt.Errorf("%s %s: %w", e, archive.URL, err)
			}

			// check file integrity only if checksum is not empty.
			if len(archive.Checksum.Sum) > 0 {
				checksum, err := utils.RootFileChecksum(fetcher.sharedVolumePath, tmpPath)
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
	if match, _ := utils.IsZipInRoot(ctx, fetcher.sharedVolumePath, tmpPath); match && !req.KeepArchive {
		// unarchive tmp file to a tmp unarchive path
		tmpUnarchivePath := filepath.Join(fetcher.sharedVolumePath, uuid.NewString())
		err := utils.UnarchiveInRoot(ctx, fetcher.sharedVolumePath, tmpPath, tmpUnarchivePath)
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

			secretDir, err := utils.RootJoin(fetcher.sharedSecretPath, filepath.Join(secret.Namespace, secret.Name))
			if err != nil {
				logger.Error(err, "directory", secretDir, "secret_name", secret.Name, "secret_namespace", secret.Namespace)
				return http.StatusBadRequest, fmt.Errorf("%w, request: %v", err, secret)
			}

			err = utils.RootMkdirAll(fetcher.sharedSecretPath, secretDir, 0750)
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

			configDir, err := utils.RootJoin(fetcher.sharedConfigPath, filepath.Join(config.Namespace, config.Name))
			if err != nil {
				logger.Error(err, "directory", configDir, "config_map_name", config.Name, "config_map_namespace", config.Namespace)
				return http.StatusBadRequest, fmt.Errorf("%w, request: %v", err,
					config)
			}

			err = utils.RootMkdirAll(fetcher.sharedConfigPath, configDir, 0750)
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

	srcFilepath, err := utils.RootJoin(fetcher.sharedVolumePath, req.Filename)
	if err != nil {
		logger.Error(err, "error sanitizing file path")
		http.Error(w, fmt.Sprintf("%s: %v", err, req.Filename), http.StatusBadRequest)
		return
	}
	dstFilepath, err := utils.RootJoin(fetcher.sharedVolumePath, req.Filename+".zip")
	if err != nil {
		logger.Error(err, "error sanitizing file path")
		http.Error(w, fmt.Sprintf("%s: %v", err, req.Filename), http.StatusBadRequest)
		return
	}

	// The artifact is deleted only after a SUCCESSFUL upload: the buildermgr
	// client retries /upload on 5xx, and deleting on failure would turn every
	// retry into "no such file" — masking the real error (registry auth, TLS,
	// storage outage) from the user's build log.
	uploadSucceeded := false
	defer func() {
		if !uploadSucceeded {
			return
		}
		errC := utils.DeleteOldPackages(srcFilepath, "DEPLOY_PKG")
		if errC != nil {
			m := "error deleting deploy package after upload"
			logger.Error(errC, m)
		}
	}()

	// OCI publish mode (RFC-0012 producer): push the deployment DIRECTORY
	// (pre-zip) as a single-layer image. Success short-circuits the
	// storagesvc upload entirely; failure either fails the request (strict)
	// or falls through to the tarball path with the push error reported
	// alongside, so the caller can mark the Package's publish as degraded.
	var ociPushErr error
	if req.OCIPush != nil {
		ociArchive, pushErr := fetcher.pushOCI(ctx, srcFilepath, req.OCIPush)
		if pushErr == nil {
			logger.Info("published deployment package as OCI image",
				"image", ociArchive.Image, "digest", ociArchive.Digest)
			uploadSucceeded = true
			writeUploadResponse(logger, w, &ArchiveUploadResponse{OCI: ociArchive})
			return
		}
		if !req.OCIPush.FallbackToStorage {
			e := "error publishing deployment package as OCI image"
			logger.Error(pushErr, e, "repository", req.OCIPush.Repository)
			http.Error(w, fmt.Sprintf("%s: %v", e, pushErr), http.StatusInternalServerError)
			return
		}
		logger.Error(pushErr, "OCI publish failed; falling back to the storagesvc tarball upload",
			"repository", req.OCIPush.Repository)
		ociPushErr = pushErr
	}

	if req.ArchivePackage {
		err = utils.ArchiveInRoot(ctx, fetcher.sharedVolumePath, srcFilepath, dstFilepath)
		if err != nil {
			e := "error archiving zip file"
			logger.Error(err, e, "source", srcFilepath, "destination", dstFilepath)
			http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
			return
		}
	} else {
		err = utils.RootRename(fetcher.sharedVolumePath, srcFilepath, dstFilepath)
		if err != nil {
			e := "error renaming the archive"
			logger.Error(err, e, "source", srcFilepath, "destination", dstFilepath)
			http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
			return
		}
	}

	logger.Info("starting upload...")
	ssClient := storageSvcClient.MakeClientWithTransport(req.StorageSvcUrl,
		storageSigningTransport(otelhttp.NewTransport(http.DefaultTransport), fetcher.Info.Namespace))

	// Open the archive once through an os.Root rooted at the shared volume so
	// the request-derived path cannot escape it (CWE-22), then hand the open
	// file to the storage client.
	uploadFile, err := utils.RootOpen(fetcher.sharedVolumePath, dstFilepath)
	if err != nil {
		e := "error opening zip file for upload"
		logger.Error(err, e, "file", dstFilepath)
		http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
		return
	}
	uploadInfo, err := uploadFile.Stat()
	if err != nil {
		uploadFile.Close()
		e := "error stating zip file for upload"
		logger.Error(err, e, "file", dstFilepath)
		http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
		return
	}
	fileID, err := ssClient.UploadReader(ctx, dstFilepath, uploadFile, uploadInfo.Size(), nil)
	uploadFile.Close()
	if err != nil {
		e := "error uploading zip file"
		logger.Error(err, e, "file", dstFilepath)
		http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
		return
	}

	sum, err := utils.RootFileChecksum(fetcher.sharedVolumePath, dstFilepath)
	if err != nil {
		e := "error calculating checksum of zip file"
		logger.Error(err, e, "file", dstFilepath)
		http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
		return
	}

	resp := &ArchiveUploadResponse{
		ArchiveDownloadUrl: ssClient.GetUrl(fileID),
		Checksum:           *sum,
	}
	if ociPushErr != nil {
		resp.OCIPushError = ociPushErr.Error()
	}
	uploadSucceeded = true
	logger.Info("completed upload request")
	writeUploadResponse(logger, w, resp)
}

// writeUploadResponse marshals and writes an upload response.
func writeUploadResponse(logger logr.Logger, w http.ResponseWriter, resp *ArchiveUploadResponse) {
	rBody, err := json.Marshal(resp)
	if err != nil {
		e := "error encoding upload response"
		logger.Error(err, e)
		http.Error(w, fmt.Sprintf("%s: %v", e, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(rBody); err != nil {
		logger.Error(err, "error writing response")
	}
}

func (fetcher *Fetcher) rename(src string, dst string) error {
	// src and dst are always under the shared volume; confine the rename to it.
	err := utils.RootRename(fetcher.sharedVolumePath, src, dst)
	if err != nil {
		return fmt.Errorf("failed to move file: %w", err)
	}
	return nil
}

// getPkgInformation gets package information from k8s api server.
func (fetcher *Fetcher) getPkgInformation(ctx context.Context, req FunctionFetchRequest) (pkg *fv1.Package, err error) {
	logger := otelUtils.LoggerWithTraceID(ctx, fetcher.logger)
	// Each error class consumes its own schedule; when a class's schedule is
	// empty its budget is spent and the error is returned.
	notFound, dial, transient := pkgNotFoundRetrySchedule, pkgDialRetrySchedule, pkgTransientRetrySchedule
	for attempt := 0; ; attempt++ {
		otelUtils.SpanTrackEvent(ctx, "fetchPkgInfo", otelUtils.MapToAttributes(map[string]string{
			"package_name":      req.Package.Name,
			"package_namespace": req.Package.Namespace,
			"retry_count":       strconv.Itoa(attempt),
		})...)
		// TODO: pass resource version in the GetOptions, added warning for now
		pkg, err = fetcher.fissionClient.CoreV1().Packages(req.Package.Namespace).Get(ctx, req.Package.Name, metav1.GetOptions{})
		if err == nil {
			if req.Package.ResourceVersion != pkg.ResourceVersion {
				logger.Info("package resource version mismatch", "pkgName", req.Package.Name, "pkgNamespace", req.Package.Namespace, "pkgResourceVersion", req.Package.ResourceVersion, "fetchedResourceVersion", pkg.ResourceVersion)
			}
			return pkg, nil
		}
		// Classify, then consume from that class's schedule only.
		sched := &transient
		switch netErr := network.Adapter(err); {
		case k8serr.IsNotFound(err):
			sched = &notFound
		case netErr != nil && (netErr.IsDialError() || netErr.IsConnRefusedError()):
			sched = &dial
		}
		if len(*sched) == 0 {
			return nil, err
		}
		if !sleepCtx(ctx, (*sched)[0]) {
			return nil, err
		}
		*sched = (*sched)[1:]
	}
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

	// RFC-0023: materialize the scoped state token BEFORE specializing, so
	// the SDK can read it the moment user code starts. Derived pod-locally
	// from the fetcher's own master secret — the secret and the token never
	// ride the (pod-visible) specialize request.
	if loadReq.StateKeyspace != "" {
		if err := fetcher.writeStateTokenFile(loadReq); err != nil {
			return http.StatusInternalServerError, fmt.Errorf("error writing state token file: %w", err)
		}
	}

	// Specialize the pod

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
		specializeURL = fmt.Sprintf("http://127.0.0.1:%d/v2/specialize", svcinfo.PortEnvRuntime)
		reader = bytes.NewReader(loadPayload)
		logger.Info("calling environment v2 specialization endpoint")
	} else {
		contentType = "text/plain"
		specializeURL = fmt.Sprintf("http://127.0.0.1:%d/specialize", svcinfo.PortEnvRuntime)
		reader = bytes.NewReader([]byte{})
		logger.Info("calling environment v1 specialization endpoint")
	}

	deadline := time.Now().Add(envSpecializeWaitBudget)
	for attempt := 0; ; attempt++ {
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
			if ctx.Err() == nil && time.Now().Before(deadline) {
				// Retries are frequent now, so log the wait once per ~10s of
				// capped delay instead of once per attempt.
				if attempt%20 == 0 {
					logger.Error(netErr, "error connecting to function environment pod for specialization request, retrying")
				}
				if !sleepCtx(ctx, envSpecializeRetryDelay(attempt)) {
					return http.StatusInternalServerError, fmt.Errorf("error specializing function pod: %w", ctx.Err())
				}
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
}

// WsStartHandler is used to generate websocket events in Kubernetes
// writeStateTokenFile derives the RFC-0023 keyspace token from the fetcher's
// master secret and writes it to StateTokenFileName under the shared mount
// (0444: the env container runs as a different user and only ever reads it).
// Without a master secret (dev clusters) it writes a placeholder — statesvc's
// pass-through mode accepts any bearer, and the SDK contract stays uniform.
func (fetcher *Fetcher) writeStateTokenFile(loadReq FunctionLoadRequest) error {
	if loadReq.FunctionMetadata == nil {
		return errors.New("specialize request with a state keyspace but no function metadata")
	}
	token := "dev-unauthenticated"
	if master := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET")); len(master) > 0 {
		token = hmacauth.EncodeKeyForEnv(hmacauth.DeriveStateKeyspaceKey(master,
			loadReq.FunctionMetadata.Namespace, loadReq.StateKeyspace))
	}
	path := filepath.Join(fetcher.sharedVolumePath, StateTokenFileName)
	// The file is read-only, so a re-specialize (infinite-functions pools,
	// retried specialization) cannot overwrite in place — replace it.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(token), 0444)
}

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
