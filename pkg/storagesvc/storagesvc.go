// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package storagesvc

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"errors"

	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"

	hmacauth "github.com/fission/fission/pkg/auth/hmac"
	"github.com/fission/fission/pkg/crd"
	"github.com/fission/fission/pkg/utils/httpmux"
	"github.com/fission/fission/pkg/utils/httpsecurity"
	"github.com/fission/fission/pkg/utils/httpserver"
	"github.com/fission/fission/pkg/utils/metrics"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

type (
	// Storage is an interface to force storage level details implementation.
	Storage interface {
		getStorageType() StorageType
		dial() (objectStore, error)
		getSubDir() string
		getContainerName() string
		// getUploadFileName returns the storage name for a new upload. When
		// namespace is non-empty the id is namespace-scoped (carries the tenant
		// marker + namespace); an empty namespace yields the legacy unscoped id.
		getUploadFileName(namespace string) (string, error)
	}

	// StorageService is a struct to hold all things for storage service
	StorageService struct {
		logger        logr.Logger
		storageClient *StorageClient
		port          int
		// authSecret, if non-empty, enables HMAC enforcement on /v1/archive.
		authSecret []byte
		// authSecretOld is accepted alongside authSecret during rotation.
		authSecretOld []byte
		// maxUploadBytes is passed to VerifierOpts.MaxBodyBytes for /v1/archive;
		// 0 (the default, and the only non-positive value maxUploadBytesFromEnv
		// produces) means hmacauth.DefaultMaxBodyBytes (256 MiB).
		maxUploadBytes int64
	}

	UploadResponse struct {
		ID string `json:"id"`
	}
)

// Functions handling storage interface
func getStorageType(storage Storage) string {
	return string(storage.getStorageType())
}

func (ss *StorageService) listItems(w http.ResponseWriter, r *http.Request) {
	logger := otelUtils.LoggerWithTraceID(r.Context(), ss.logger)
	// get all archives on storage
	// out of them, there may be some just created but not referenced by packages yet.
	// need to filter them out.
	archivesInStorage, err := ss.storageClient.getItemIDsWithFilter(ss.storageClient.filterAllItems, false)
	if err != nil {
		logger.Error(err, "error getting items from storage")
		return
	}
	// A namespace-scoped caller sees only its own archives; an unrestricted
	// (master) caller — the pruner, the CLI — sees everything, unchanged.
	// Deliberately stricter than authorizedFor: list does NOT surface legacy/
	// unscoped archives to a tenant (it shows only what the tenant owns), whereas
	// get/delete/info grandfather legacy ids. The asymmetry leaks nothing — list
	// is the more restrictive direction.
	if authNS, _ := hmacauth.AuthenticatedNamespace(r.Context()); authNS != "" {
		scoped := make([]string, 0, len(archivesInStorage))
		for _, id := range archivesInStorage {
			if archiveNamespace(id) == authNS {
				scoped = append(scoped, id)
			}
		}
		archivesInStorage = scoped
	}
	logger.V(1).Info("archives in storage", "archives", archivesInStorage)

	// respond with the list of items
	resp, err := json.Marshal(archivesInStorage)
	if err != nil {
		http.Error(w, "error marshaling item list", http.StatusInternalServerError)
		return
	}
	_, err = w.Write(resp)
	if err != nil {
		logger.Error(err,
			"error writing HTTP response")
	}
}

// Handle multipart file uploads.
func (ss *StorageService) uploadHandler(w http.ResponseWriter, r *http.Request) {
	logger := otelUtils.LoggerWithTraceID(r.Context(), ss.logger)

	// handle upload
	err := r.ParseMultipartForm(0)
	if err != nil {
		http.Error(w, "failed to parse request", http.StatusBadRequest)
		return
	}
	file, handler, err := r.FormFile("uploadfile")
	if err != nil {
		http.Error(w, "missing upload file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// the backend needs the file size, but that's different from the
	// content length, the content length being the size of the
	// encoded file in the HTTP request. So we require an
	// "X-File-Size" header in bytes.

	fileSizeS, ok := r.Header["X-File-Size"]
	if !ok {
		logger.Error(nil, "upload is missing the 'X-File-Size' header",
			"filename", handler.Filename)
		http.Error(w, "missing X-File-Size header", http.StatusBadRequest)
		return
	}

	fileSize, err := strconv.Atoi(fileSizeS[0])
	if err != nil {
		logger.Error(err, "error parsing 'X-File-Size' header", "header", fileSizeS,
			"filename", handler.Filename)
		http.Error(w, "missing or bad X-File-Size header", http.StatusBadRequest)
		return
	}

	// TODO: allow headers to add more metadata (e.g. environment and function metadata)
	logger.V(1).Info("handling upload",
		"filename", handler.Filename)

	// authNS is the namespace whose key verified the request (empty = master /
	// unrestricted), set by the verifier — never a raw header. A non-empty value
	// scopes the new archive's id to that tenant. Validate it before it is joined
	// into a storage path (load-bearing for S3, which has no os.Root confinement).
	authNS, _ := hmacauth.AuthenticatedNamespace(r.Context())
	if authNS != "" && !validNamespaceLabel(authNS) {
		http.Error(w, "invalid namespace", http.StatusBadRequest)
		return
	}

	id, err := ss.storageClient.putFile(file, int64(fileSize), authNS)
	if err != nil {
		logger.Error(err, "error saving uploaded file", "filename", handler.Filename)
		http.Error(w, "Error saving uploaded file", http.StatusInternalServerError)
		return
	}

	totalMemoryUsage.Add(r.Context(), int64(fileSize))

	// respond with an ID that can be used to retrieve the file
	ur := &UploadResponse{
		ID: id,
	}
	resp, err := json.Marshal(ur)
	if err != nil {
		logger.Error(err, "error marshaling uploaded file response", "filename", handler.Filename)
		http.Error(w, "Error marshaling response", http.StatusInternalServerError)
		return
	}
	_, err = w.Write(resp)
	if err != nil {
		logger.Error(err,
			"error writing HTTP response", "filename", handler.Filename,
		)
	}

	totalArchives.Add(r.Context(), 1)
}

// getOrListHandler dispatches GET /v1/archive: a request carrying an `id` query
// param downloads that archive, otherwise it lists archives. stdlib-style
// routing cannot match on query params (gorilla used .Queries("id", "{id}") to
// split these), so the two GET routes merge here.
func (ss *StorageService) getOrListHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("id") {
		ss.downloadHandler(w, r)
		return
	}
	ss.listItems(w, r)
}

func (ss *StorageService) getIdFromRequest(r *http.Request) (string, error) {
	values := r.URL.Query()
	ids, ok := values["id"]
	if !ok || len(ids) == 0 {
		return "", errors.New("missing `id' query param")
	}
	return ids[0], nil
}

func (ss *StorageService) deleteHandler(w http.ResponseWriter, r *http.Request) {
	// get id from request
	fileId, err := ss.getIdFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// A namespace-scoped caller may only delete its own (or legacy) archives.
	// 404, not 403, so it cannot probe whether another tenant's archive exists.
	if !ss.authorizedFor(r, fileId) {
		http.Error(w, "Error deleting item: not found", http.StatusNotFound)
		return
	}

	filesize, err := ss.storageClient.getFileSize(fileId)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err = ss.storageClient.removeFileByID(fileId)
	if err != nil {
		msg := fmt.Sprintf("Error deleting item: %v", err)
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}

	totalArchives.Add(r.Context(), -1)
	totalMemoryUsage.Add(r.Context(), -filesize)
	w.WriteHeader(http.StatusOK)
}

func (ss *StorageService) downloadHandler(w http.ResponseWriter, r *http.Request) {
	logger := otelUtils.LoggerWithTraceID(r.Context(), ss.logger)

	// get id from request
	fileId, err := ss.getIdFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// A namespace-scoped caller may only download its own (or legacy) archives.
	// 404, not 403, so it cannot probe whether another tenant's archive exists.
	if !ss.authorizedFor(r, fileId) {
		http.Error(w, "Error retrieving item: not found", http.StatusNotFound)
		return
	}

	// Get the file, open it, and stream it to the response.
	err = ss.storageClient.copyFileToStream(fileId, w)
	if err != nil {
		logger.Error(err, "error getting file from storage client", "file_id", fileId)
		switch err {
		case ErrNotFound:
			http.Error(w, "Error retrieving item: not found", http.StatusNotFound)
		case ErrRetrievingItem:
			http.Error(w, "Error retrieving item", http.StatusBadRequest)
		case ErrWritingFileIntoResponse:
			http.Error(w, "Error writing response", http.StatusInternalServerError)
		}
		return
	}
}

func (ss *StorageService) infoHandler(w http.ResponseWriter, r *http.Request) {

	fileID, err := ss.getIdFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// A namespace-scoped caller may only probe its own (or legacy) archives;
	// deny as 404 (identical to a real miss) so it is not an existence oracle.
	if !ss.authorizedFor(r, fileID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	err = ss.storageClient.exists(fileID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	storageType := ss.storageClient.config.storage.getStorageType()
	if storageType == StorageTypeS3 {
		w.Header().Add("X-FISSION-BUCKET", ss.storageClient.config.storage.getContainerName())
	}
	w.Header().Add("X-FISSION-STORAGETYPE", string(storageType))
}

func (ss *StorageService) healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func MakeStorageService(logger logr.Logger, storageClient *StorageClient, port int, authSecret, authSecretOld []byte, maxUploadBytes int64) *StorageService {
	return &StorageService{
		logger:         logger.WithName("storage_service"),
		storageClient:  storageClient,
		port:           port,
		authSecret:     authSecret,
		authSecretOld:  authSecretOld,
		maxUploadBytes: maxUploadBytes,
	}
}

func (ss *StorageService) Start(ctx context.Context, mgr *errgroup.Group, port int, listener net.Listener) {
	httpserver.Serve(ctx, ss.logger, mgr, httpserver.ServerOptions{
		Name: "storagesvc", Addr: strconv.Itoa(port), Listener: listener, Handler: ss.makeHandler(),
	})
}

// makeHandler builds the storagesvc HTTP handler chain (auth + routes +
// security headers + OTEL). Split out from Start so the wiring — in particular
// the configurable upload body cap — is exercisable without binding a listener.
func (ss *StorageService) makeHandler() http.Handler {
	// Per-route metrics always; the HMAC verifier wraps the whole mux as the
	// OUTERMOST middleware (so 401s are handled at the auth layer and metrics
	// see only post-verification requests) only when a master is configured.
	opts := []httpmux.Option{httpmux.WithMetrics(metrics.HTTPRecorder{})}
	if len(ss.authSecret) > 0 {
		// HMAC enforcement is opt-in via the FISSION_INTERNAL_AUTH_SECRET env
		// var; an empty master means the verifier is not registered at all
		// (backwards-compatible with pre-1.(N+1) installs).
		//
		// The verifier derives the per-service key for ServiceStoragesvc rather
		// than using the master directly, so a leak of this server's memory
		// cannot forge requests on other Fission internal channels (fetcher,
		// builder, executor, router-internal). See docs/internal-auth/00-design.md.
		//
		// NamespaceFromHeader additionally scopes the key per tenant: a caller
		// that sends X-Fission-Auth-Namespace is verified with that namespace's
		// derived key, so a leaked tenant fetcher key can talk to storagesvc only
		// as its own namespace. It stays backward-compatible by also accepting the
		// master-derived key for callers that send no namespace header (existing
		// fetchers, the CLI, the pruner), so this is safe to adopt unconditionally
		// — multi-namespace tenancy doesn't have to be enabled for it to be inert.
		opts = append(opts, httpmux.WithMiddleware(hmacauth.ServiceVerifierNamespaceFromHeader(ss.authSecret, ss.authSecretOld, hmacauth.ServiceStoragesvc, hmacauth.VerifierOpts{
			SkewSec: 60,
			Bypass:  []string{"/healthz"},
			// Caps the body the verifier buffers to check its signature; the
			// verifier resolves 0 to 256 MiB. Operator-tunable via
			// STORAGE_MAX_ARCHIVE_SIZE_MIB — see the maxUploadBytes field and values.yaml.
			MaxBodyBytes: ss.maxUploadBytes,
			Logger:       ss.logger.WithName("hmac"),
		})))
	}
	m := httpmux.New(opts...)
	m.HandleFunc("/v1/archive", ss.uploadHandler).Methods("POST")
	// stdlib-style matching can't key on the ?id= query param (gorilla used
	// .Queries to split these), so the download (GET with id) and list (GET
	// without) handlers merge into getOrListHandler.
	m.HandleFunc("/v1/archive", ss.getOrListHandler).Methods("GET")
	m.HandleFunc("/v1/archive", ss.deleteHandler).Methods("DELETE")
	m.HandleFunc("/v1/archive", ss.infoHandler).Methods("HEAD")
	m.HandleFunc("/healthz", ss.healthHandler).Methods("GET")

	// Storagesvc is router/builder/function-pod internal per
	// charts/fission-all/templates/storagesvc/networkpolicy.yaml.
	// SecurityHeaders + DenyAllCORS as defense-in-depth: a future
	// regression exposing this port via Ingress must not become a
	// browser-readable archive store.
	return httpsecurity.SecurityHeaders(
		httpsecurity.DenyAllCORS(
			otelUtils.GetHandlerWithOTEL(m.Handler(), "fission-storagesvc", otelUtils.UrlsToIgnore("/healthz")),
		),
	)
}

// maxArchiveSizeMiBCeil is the largest STORAGE_MAX_ARCHIVE_SIZE_MIB we accept
// before the `<< 20` MiB→bytes conversion would overflow int64; larger values
// are treated as misconfiguration and fall back to the default cap.
const maxArchiveSizeMiBCeil = math.MaxInt64 >> 20

// maxUploadBytesFromEnv reads STORAGE_MAX_ARCHIVE_SIZE_MIB — a plain integer
// number of MiB, no unit suffix — and returns the corresponding /v1/archive
// upload body cap in bytes. Unset, zero, or invalid values return 0, which the
// verifier resolves to hmacauth.DefaultMaxBodyBytes (256 MiB). The cap bounds
// the multipart-wrapped request body, marginally larger than the raw archive.
func maxUploadBytesFromEnv(logger logr.Logger) int64 {
	v := strings.TrimSpace(os.Getenv("STORAGE_MAX_ARCHIVE_SIZE_MIB"))
	if v == "" {
		return 0
	}
	defaultMiB := hmacauth.DefaultMaxBodyBytes >> 20
	mib, err := strconv.ParseInt(v, 10, 64)
	switch {
	case err != nil:
		logger.Error(err, "STORAGE_MAX_ARCHIVE_SIZE_MIB is not a plain integer number of MiB; using the default upload cap",
			"value", v, "defaultMiB", defaultMiB)
		return 0
	case mib < 0:
		logger.Info("ignoring negative STORAGE_MAX_ARCHIVE_SIZE_MIB; using the default upload cap",
			"value", v, "defaultMiB", defaultMiB)
		return 0
	case mib > maxArchiveSizeMiBCeil:
		logger.Info("STORAGE_MAX_ARCHIVE_SIZE_MIB is too large; using the default upload cap",
			"value", v, "maxMiB", maxArchiveSizeMiBCeil, "defaultMiB", defaultMiB)
		return 0
	}
	return mib << 20
}

// Start runs storage service on the given port. See StartWithOptions.
func Start(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, storage Storage, mgr *errgroup.Group, port int) error {
	return StartWithOptions(ctx, clientGen, logger, storage, mgr, Options{Port: port})
}

// Options configures StartWithOptions. The API listener is either pre-bound
// by the caller (Listener — e.g. a test harness binding 127.0.0.1:0) or
// bound here from Port.
type Options struct {
	// Port is the storage service API port. Ignored when Listener is set.
	Port int
	// Listener optionally pre-binds the API listener.
	Listener net.Listener
}

// StartWithOptions runs the storage service with an injectable listener.
func StartWithOptions(ctx context.Context, clientGen crd.ClientGeneratorInterface, logger logr.Logger, storage Storage, mgr *errgroup.Group, opts Options) error {
	enablePruner, err := strconv.ParseBool(os.Getenv("PRUNE_ENABLED"))
	if err != nil {
		logger.Error(err, "PRUNE_ENABLED value not set. Enabling archive pruner by default.")
		enablePruner = true
	}
	// create a storage client
	storageClient, err := MakeStorageClient(logger, storage)
	if err != nil {
		return fmt.Errorf("error creating storageClient: %w", err)
	}

	// Read the shared HMAC secret from the env (the design at docs/internal-auth/00-design.md). Empty means the
	// verifier middleware is not registered, preserving backwards compat.
	authSecret := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET"))
	authSecretOld := []byte(os.Getenv("FISSION_INTERNAL_AUTH_SECRET_OLD"))

	// create http handlers
	storageService := MakeStorageService(logger, storageClient, opts.Port, authSecret, authSecretOld, maxUploadBytesFromEnv(logger))
	mgr.Go(func() error {
		metrics.ServeMetrics(ctx, "storagesvc", logger, mgr)
		return nil
	})

	mgr.Go(func() error {
		storageService.Start(ctx, mgr, opts.Port, opts.Listener)
		return nil
	})

	// enablePruner prevents storagesvc unit test from needing to talk to kubernetes
	if enablePruner {
		// get the prune interval and start the archive pruner
		pruneInterval, err := strconv.Atoi(os.Getenv("PRUNE_INTERVAL"))
		if err != nil {
			pruneInterval = defaultPruneInterval
		}
		pruner, err := MakeArchivePruner(logger, clientGen, storageClient, time.Duration(pruneInterval))
		if err != nil {
			return fmt.Errorf("error creating archivePruner: %w", err)
		}
		mgr.Go(func() error {
			pruner.Start(ctx, mgr)
			return nil
		})
	}

	logger.Info("storage service started")
	return nil
}
