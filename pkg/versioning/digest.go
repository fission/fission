// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package versioning

import (
	"bytes"
	"fmt"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils"
)

// PackageDigest returns the content pin for pkg's code: the OCI digest when
// the deployment archive is OCI-backed and pinned; else the sha256 checksum
// recorded on the deployment archive (`sha256:<sum>`); else, for literal
// archives — which carry NO checksum, see UploadArchiveFile /
// pkg/fission-cli/cmd/package/util/util.go:38-40 — the sha256 computed over
// the literal bytes directly; else the same three-step chain run against the
// source archive instead; else an error (the package has no content that can
// be pinned at all, e.g. an unpinned OCI tag with no source fallback).
func PackageDigest(pkg *fv1.Package) (string, error) {
	if digest, ok, err := archiveDigest(&pkg.Spec.Deployment); err != nil {
		return "", fmt.Errorf("versioning: computing digest for package %s/%s deployment archive: %w", pkg.Namespace, pkg.Name, err)
	} else if ok {
		return digest, nil
	}

	if digest, ok, err := archiveDigest(&pkg.Spec.Source); err != nil {
		return "", fmt.Errorf("versioning: computing digest for package %s/%s source archive: %w", pkg.Namespace, pkg.Name, err)
	} else if ok {
		return digest, nil
	}

	return "", fmt.Errorf("versioning: package %s/%s has no content that can be pinned: no OCI digest, checksum, or literal contents on either the deployment or source archive", pkg.Namespace, pkg.Name)
}

// isOCIDigestBacked reports whether pkg's deployment archive is pinned by an
// immutable OCI digest. Publish treats this as the one content pin that
// needs no version-owned snapshot Package copy — everything else (a
// checksummed or literal archive) lives in a mutable Package that could be
// edited in place after publish, so Publish repoints the version's snapshot
// at a copy instead (the "legacy path", step 5 of the publish algorithm).
func isOCIDigestBacked(pkg *fv1.Package) bool {
	return pkg.Spec.Deployment.OCI != nil && pkg.Spec.Deployment.OCI.Digest != ""
}

// archiveDigest returns a's content digest and true when a carries one, in
// priority order: OCI digest, recorded sha256 checksum, sha256 computed over
// literal bytes. ok is false (with no error) when a has none of the three —
// e.g. a URL archive with no checksum recorded — which the caller treats as
// "try the next archive", not a hard failure.
func archiveDigest(a *fv1.Archive) (digest string, ok bool, err error) {
	if a.OCI != nil && a.OCI.Digest != "" {
		return a.OCI.Digest, true, nil
	}

	if a.Checksum.Type == fv1.ChecksumTypeSHA256 && a.Checksum.Sum != "" {
		return "sha256:" + a.Checksum.Sum, true, nil
	}

	if len(a.Literal) > 0 {
		sum, err := utils.GetChecksum(bytes.NewReader(a.Literal))
		if err != nil {
			return "", false, err
		}
		return "sha256:" + sum.Sum, true, nil
	}

	return "", false, nil
}
