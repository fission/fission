// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package fetcher

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// Fission-Environment interface. The following types are not
// exposed in the Fission API, but rather used by Fission to
// StateTokenFileName is the file under the shared mount (/userfunc) where the
// fetcher writes the function's scoped state token at specialize time
// (RFC-0023). It is a file, not an env var, because a poolmgr generic pod's
// user container is already running before its function identity is known —
// env vars cannot be added to a running container. The executor points the
// SDK at it via FISSION_STATE_TOKEN_PATH.
const StateTokenFileName = ".fission-state-token"

// talk to environments.
type (
	FetchRequestType int

	FunctionSpecializeRequest struct {
		FetchReq FunctionFetchRequest
		LoadReq  FunctionLoadRequest
	}

	FunctionFetchRequest struct {
		FetchType     FetchRequestType         `json:"fetchType"`
		Package       metav1.ObjectMeta        `json:"package"`
		URL           string                   `json:"url"`
		StorageSvcUrl string                   `json:"storagesvcurl"`
		Filename      string                   `json:"filename"`
		Secrets       []fv1.SecretReference    `json:"secretList"`
		ConfigMaps    []fv1.ConfigMapReference `json:"configMapList"`
		KeepArchive   bool                     `json:"keeparchive"`
	}

	FunctionLoadRequest struct {
		// FilePath is an absolute filesystem path to the
		// function. What exactly is stored here is
		// env-specific. Optional.
		FilePath string `json:"filepath"`

		// FunctionName has an environment-specific meaning;
		// usually, it defines a function within a module
		// containing multiple functions. Optional; default is
		// environment-specific.
		FunctionName string `json:"functionName"`

		// URL to expose this function at. Optional; defaults
		// to "/".
		URL string `json:"url"`

		// Metadata
		FunctionMetadata *metav1.ObjectMeta

		EnvVersion int `json:"envVersion"`

		// StateKeyspace, when non-empty, is the RFC-0023 keyspace this
		// function is entitled to: before specializing, the fetcher derives
		// the scoped state token from its own master secret and writes it to
		// StateTokenFileName under the shared mount for the user container's
		// SDK to read. Only the NON-SECRET keyspace name rides this request
		// (it appears in the pod-visible -specialize-request arg); the token
		// itself never does.
		StateKeyspace string `json:"stateKeyspace,omitempty"`
	}

	// ArchiveUploadRequest send from builder manager describes which
	// deployment package should be upload to storage service.
	ArchiveUploadRequest struct {
		Filename       string `json:"filename"`
		StorageSvcUrl  string `json:"storagesvcurl"`
		ArchivePackage bool   `json:"archivepackage"`
		// OCIPush (RFC-0012 producer) asks the fetcher to publish the built
		// deployment directory as a single-layer OCI image instead of the
		// storagesvc tarball. On push failure with FallbackToStorage the
		// fetcher falls back to the tarball upload and reports the push
		// error alongside; without it the upload fails.
		OCIPush *OCIPushSpec `json:"ociPush,omitempty"`
	}

	// OCIPushSpec carries the registry destination and credentials selector
	// for an OCI publish (RFC-0012).
	OCIPushSpec struct {
		// Repository is the full repository (no tag), e.g.
		// ghcr.io/org/fission-packages/default/pkg; the fetcher tags the
		// image with its own short digest.
		Repository string `json:"repository"`
		// PublishedRepository, when set, is the repository recorded in the
		// published archive INSTEAD of Repository (the push endpoint). For
		// registries whose push URL differs from the consumption URL — the
		// RFC-0012 split-brain: the kubelet (image volumes) resolves via the
		// NODE resolver, the builder pod via cluster DNS. Same registry,
		// two names; the digest pins identity across both.
		PublishedRepository string `json:"publishedRepository,omitempty"`
		// PushSecretName names a dockerconfigjson secret in the fetcher's
		// namespace holding write credentials; empty = anonymous/ambient.
		PushSecretName string `json:"pushSecretName,omitempty"`
		// InsecureHosts is a host (host[:port]) allowlist permitted plain
		// HTTP, merged with FETCHER_ALLOW_INSECURE_REGISTRIES.
		InsecureHosts []string `json:"insecureHosts,omitempty"`
		// FallbackToStorage selects the degraded mode: push failure falls
		// back to the storagesvc tarball (response carries OCIPushError)
		// instead of failing the upload.
		FallbackToStorage bool `json:"fallbackToStorage"`
	}

	// ArchiveUploadResponse defines the built artifact's location: an OCI
	// image reference (RFC-0012 producer) or a storagesvc download url and
	// checksum.
	ArchiveUploadResponse struct {
		ArchiveDownloadUrl string       `json:"archiveDownloadUrl,omitempty"`
		Checksum           fv1.Checksum `json:"checksum"`
		// OCI is set when the artifact was published as an OCI image
		// (digest-pinned; ImagePullSecrets are stamped by the caller).
		OCI *fv1.OCIArchive `json:"oci,omitempty"`
		// OCIPushError carries the publish failure when the response fell
		// back to the storagesvc tarball, so the caller can surface the
		// degradation on the Package.
		OCIPushError string `json:"ociPushError,omitempty"`
	}
)
