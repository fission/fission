// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/go-logr/logr"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/fetcher"
)

// packageRegistryConfig is the RFC-0012 producer configuration (the chart's
// packageRegistry block, delivered via PACKAGE_REGISTRY_* env vars). When
// enabled, every successful build publishes its deployment archive as a
// digest-pinned single-layer OCI image and the Package is rewritten to
// Archive{Type: oci}; without it the tarball/storagesvc pipeline runs
// unchanged.
type packageRegistryConfig struct {
	enabled          bool
	repositoryPrefix string
	// publishedPrefix, when set, replaces repositoryPrefix in the RECORDED
	// image reference (push goes to repositoryPrefix). For registries whose
	// push URL differs from the consumption URL (node-resolvable vs cluster
	// DNS — the split-brain risk).
	publishedPrefix   string
	pushSecret        string
	pullSecret        string
	insecureHosts     []string
	fallbackToStorage bool
}

// packageDeliveryAnnotation is the per-package producer escape hatch: a
// Package annotated `fission.io/package-delivery: tarball` keeps the
// storagesvc tarball output even with a registry configured.
const (
	packageDeliveryAnnotation = "fission.io/package-delivery"
	packageDeliveryTarball    = "tarball"
)

// loadPackageRegistryConfig parses the producer configuration. enabled
// hard-requires a repository prefix (a half-configured producer must fail
// loudly at startup, not at first build); the booleans soft-fail to their
// defaults.
func loadPackageRegistryConfig(logger logr.Logger) (*packageRegistryConfig, error) {
	cfg := &packageRegistryConfig{fallbackToStorage: true}
	raw := os.Getenv("PACKAGE_REGISTRY_ENABLED")
	if raw == "" {
		return cfg, nil
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		// Hard-fail, same policy as the missing prefix: an unparseable
		// enable flag silently shipping tarballs forever is exactly the
		// half-configured-producer failure mode startup must reject.
		return nil, fmt.Errorf("failed to parse 'PACKAGE_REGISTRY_ENABLED' value %q: %w", raw, err)
	}
	cfg.enabled = enabled
	if !enabled {
		return cfg, nil
	}

	cfg.repositoryPrefix = strings.TrimSuffix(strings.TrimSpace(os.Getenv("PACKAGE_REGISTRY_REPOSITORY_PREFIX")), "/")
	if cfg.repositoryPrefix == "" {
		return nil, fmt.Errorf("'PACKAGE_REGISTRY_ENABLED' is set but 'PACKAGE_REGISTRY_REPOSITORY_PREFIX' is empty; the producer needs a repository prefix (e.g. ghcr.io/org/fission-packages)")
	}
	cfg.publishedPrefix = strings.TrimSuffix(strings.TrimSpace(os.Getenv("PACKAGE_REGISTRY_PUBLISHED_PREFIX")), "/")
	cfg.pushSecret = strings.TrimSpace(os.Getenv("PACKAGE_REGISTRY_PUSH_SECRET"))
	cfg.pullSecret = strings.TrimSpace(os.Getenv("PACKAGE_REGISTRY_PULL_SECRET"))
	for h := range strings.SplitSeq(os.Getenv("PACKAGE_REGISTRY_INSECURE_HOSTS"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			cfg.insecureHosts = append(cfg.insecureHosts, h)
		}
	}
	effective := cfg.publishedPrefix
	if effective == "" {
		effective = cfg.repositoryPrefix
	}
	if host, _, _ := strings.Cut(effective, "/"); strings.HasSuffix(strings.Split(host, ":")[0], ".svc") || strings.Contains(host, ".svc.") {
		logger.Info("WARNING: packages will reference a cluster-DNS registry name that nodes cannot resolve; image-volume mounts will fail — set PACKAGE_REGISTRY_PUBLISHED_PREFIX to a node-resolvable name",
			"recordedPrefix", effective)
	}
	if raw := os.Getenv("PACKAGE_REGISTRY_FALLBACK_TO_STORAGE"); raw != "" {
		fallback, err := strconv.ParseBool(raw)
		if err != nil {
			logger.Error(err, "failed to parse 'PACKAGE_REGISTRY_FALLBACK_TO_STORAGE' - keeping the default (true)", "value", raw)
		} else {
			cfg.fallbackToStorage = fallback
		}
	}
	return cfg, nil
}

// ociPushSpecFor returns the upload request's push spec for one package, or
// nil when the build must stay on the tarball path: producer off, KeepArchive
// env (expects an archive FILE; OCI artifacts are directory-shaped), or the
// per-package tarball annotation.
func (cfg *packageRegistryConfig) ociPushSpecFor(pkg *fv1.Package, env *fv1.Environment) *fetcher.OCIPushSpec {
	if cfg == nil || !cfg.enabled {
		return nil
	}
	if env.Spec.KeepArchive {
		return nil
	}
	if pkg.Annotations[packageDeliveryAnnotation] == packageDeliveryTarball {
		return nil
	}
	spec := &fetcher.OCIPushSpec{
		Repository:        fmt.Sprintf("%s/%s/%s", cfg.repositoryPrefix, pkg.Namespace, pkg.Name),
		PushSecretName:    cfg.pushSecret,
		InsecureHosts:     cfg.insecureHosts,
		FallbackToStorage: cfg.fallbackToStorage,
	}
	if cfg.publishedPrefix != "" {
		spec.PublishedRepository = fmt.Sprintf("%s/%s/%s", cfg.publishedPrefix, pkg.Namespace, pkg.Name)
	}
	return spec
}
