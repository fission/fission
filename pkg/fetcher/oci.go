// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fetcher

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	apiv1 "k8s.io/api/core/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/oci"
	"github.com/fission/fission/pkg/utils"
	otelUtils "github.com/fission/fission/pkg/utils/otel"
)

// fetchOCI materializes an OCI deployment archive (RFC-0001 Path A): it pulls
// the image and extracts its filesystem into a temporary directory under the
// shared volume, then atomically renames it to storePath — the same
// tmp-then-rename contract as the tarball path, so the existing
// storePath-exists early-exit in Fetch keeps fetches idempotent. KeepArchive
// and checksums don't apply: there is no archive file, and integrity pinning
// is the OCI digest.
func (fetcher *Fetcher) fetchOCI(ctx context.Context, pkg *fv1.Package, oa *fv1.OCIArchive, storePath string) (int, error) {
	logger := otelUtils.LoggerWithTraceID(ctx, fetcher.logger)
	otelUtils.SpanTrackEvent(ctx, "fetchOCIImage", otelUtils.GetAttributesForPackage(pkg)...)

	keychain, err := oci.Keychain(ctx, fetcher.kubeClient, fetcher.Info.Namespace, fv1.FissionFetcherSA, oa.ImagePullSecrets)
	if err != nil {
		logger.Error(err, "error building registry keychain", "image", oa.Image)
		return http.StatusInternalServerError, err
	}

	tmpDir := uuid.NewString()
	err = oci.ExtractImage(ctx, oa.Image, fetcher.sharedVolumePath, tmpDir, oci.ExtractOptions{
		SubPath:            oa.SubPath,
		Digest:             oa.Digest,
		Keychain:           keychain,
		InsecureRegistries: insecureRegistriesFromEnv(),
	})
	if err != nil {
		// Best-effort cleanup; an orphaned tmp dir is harmless (never
		// referenced again) but wastes volume space.
		if rmErr := os.RemoveAll(filepath.Join(fetcher.sharedVolumePath, tmpDir)); rmErr != nil {
			logger.Error(rmErr, "error cleaning up partial extraction", "dir", tmpDir)
		}
		logger.Error(err, "error extracting OCI image", "image", oa.Image)
		return http.StatusInternalServerError, fmt.Errorf("error extracting OCI image %s: %w", oa.Image, err)
	}

	if err := fetcher.rename(filepath.Join(fetcher.sharedVolumePath, tmpDir), storePath); err != nil {
		logger.Error(err, "error renaming extracted image", "original_path", tmpDir, "rename_path", storePath)
		return http.StatusInternalServerError, err
	}

	otelUtils.SpanTrackEvent(ctx, "packageFetched", otelUtils.GetAttributesForPackage(pkg)...)
	logger.Info("successfully placed OCI image contents", "image", oa.Image, "location", storePath)
	return http.StatusOK, nil
}

// insecureRegistriesFromEnv parses the FETCHER_ALLOW_INSECURE_REGISTRIES
// comma-separated host allowlist (set on the fetcher container by the
// executor; see fetcher/config). Empty means registries must serve TLS (see
// oci.ExtractOptions.InsecureRegistries for the localhost/private-IP
// exceptions).
func insecureRegistriesFromEnv() []string {
	raw := os.Getenv("FETCHER_ALLOW_INSECURE_REGISTRIES")
	if raw == "" {
		return nil
	}
	var hosts []string
	for h := range strings.SplitSeq(raw, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// pushOCI publishes the built deployment directory as a single-layer OCI
// image (RFC-0012 producer). The push credential is resolved through the
// same kauth keychain as pulls, with the spec's push secret as the
// imagePullSecret-style reference.
func (fetcher *Fetcher) pushOCI(ctx context.Context, srcPath string, spec *OCIPushSpec) (*fv1.OCIArchive, error) {
	// RootStat confines the request-derived path to the shared volume
	// (os.Root; CWE-22 — same contract as the tarball upload path).
	st, err := utils.RootStat(fetcher.sharedVolumePath, srcPath)
	if err != nil {
		return nil, fmt.Errorf("stating build artifact: %w", err)
	}
	if !st.IsDir() {
		// OCI artifacts are directory-shaped (image volumes mount
		// directories); a single-file artifact stays on the tarball path.
		return nil, fmt.Errorf("build artifact %q is not a directory; OCI publishing requires a directory artifact", filepath.Base(srcPath))
	}

	var pushSecrets []apiv1.LocalObjectReference
	if spec.PushSecretName != "" {
		pushSecrets = append(pushSecrets, apiv1.LocalObjectReference{Name: spec.PushSecretName})
	}
	keychain, err := oci.Keychain(ctx, fetcher.kubeClient, fetcher.Info.Namespace, fv1.FissionFetcherSA, pushSecrets)
	if err != nil {
		return nil, fmt.Errorf("building registry keychain: %w", err)
	}

	insecure := insecureRegistriesFromEnv()
	insecure = append(insecure, spec.InsecureHosts...)

	imageRef, digest, err := oci.PushDirectory(ctx, srcPath, spec.Repository, oci.PushOptions{
		Keychain:           keychain,
		InsecureRegistries: insecure,
	})
	if err != nil {
		return nil, err
	}
	if spec.PublishedRepository != "" {
		// Record the consumption-side name (e.g. the node-resolvable form
		// the kubelet pulls); the digest pins identity across both names.
		imageRef = strings.Replace(imageRef, spec.Repository, spec.PublishedRepository, 1)
	}
	return &fv1.OCIArchive{Image: imageRef, Digest: digest}, nil
}
