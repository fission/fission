// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"crypto/sha256"
	"fmt"
	"io"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// PackageContentHash fingerprints what a package actually delivers, so a
// change in content can be detected without the CLI poking the build status
// (RFC-0029 §3).
//
// What goes in is chosen so the hash changes exactly when the delivered bytes
// could differ, and not otherwise:
//
//   - the OCI image reference and digest, for RFC-0001/0012 packages;
//   - the URL and its checksum, for archives in storagesvc;
//   - the literal bytes themselves, for inline archives.
//
// What stays out matters as much. ResourceVersion is excluded because it
// changes on every write including status updates, which would make every
// reconcile look like a content change. ImagePullSecrets and SubPath are
// excluded because they alter how the same content is reached, not what it is
// — rotating a pull secret must not rebuild the world.
//
// The empty package hashes to a stable non-empty value, so "no content" is
// still distinguishable from "not yet recorded" (the empty string).
func PackageContentHash(spec fv1.PackageSpec) string {
	h := sha256.New()
	hashArchive(h, "source", spec.Source)
	hashArchive(h, "deployment", spec.Deployment)
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// hashArchive folds one archive into h. Every field is length-prefixed so that
// concatenation can never be ambiguous — without it, an image reference of
// "a" + digest "bc" would hash identically to "ab" + "c", and two genuinely
// different packages would look unchanged.
func hashArchive(h io.Writer, label string, a fv1.Archive) {
	write(h, label)
	write(h, string(a.Type))
	if a.OCI != nil {
		write(h, "oci")
		write(h, a.OCI.Image)
		write(h, a.OCI.Digest)
	}
	write(h, a.URL)
	write(h, string(a.Checksum.Type))
	write(h, a.Checksum.Sum)
	if len(a.Literal) > 0 {
		write(h, "literal")
		writeBytes(h, a.Literal)
	}
}

func write(h io.Writer, s string) { writeBytes(h, []byte(s)) }

func writeBytes(h io.Writer, b []byte) {
	// Length prefix, then payload — see hashArchive on why.
	fmt.Fprintf(h, "%d:", len(b))
	_, _ = h.Write(b)
}
