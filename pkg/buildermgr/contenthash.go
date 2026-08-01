// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package buildermgr

import (
	"crypto/sha256"
	"fmt"
	"io"
	"strings"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// PackageContentHash fingerprints a package's INPUT content, so a change can
// be detected without the CLI poking the build status (RFC-0029 §3).
//
// Input, not output, and that distinction is the whole design. A source
// package's deployment archive is the build's own product: the builder writes
// spec.Deployment on success. Folding that into the hash would make every
// successful build look like a fresh content change and rebuild forever. So:
//
//   - a package WITH a source archive hashes the source alone — the source is
//     what the user supplies and the deployment is derived from it;
//   - a deploy-only or OCI package hashes the deployment, because there is no
//     build and the deployment IS the supplied content.
//
// Within an archive, what goes in is chosen so the hash changes exactly when
// the delivered bytes could differ: the OCI image reference and digest, the
// URL and its checksum, or the literal bytes. What stays out matters as much.
// ResourceVersion is excluded because it changes on every write including
// status updates. ImagePullSecrets is excluded because it alters how the same
// content is reached, not what it is — rotating a pull secret must not rebuild
// the world. SubPath is NOT excluded: it selects which subtree of the image
// becomes the deployment root, so it genuinely changes the delivered bytes.
//
// The empty package hashes to a stable non-empty value, so "no content" is
// still distinguishable from "not yet recorded" (the empty string).
func PackageContentHash(spec fv1.PackageSpec) string {
	h := sha256.New()
	if !spec.Source.IsEmpty() {
		hashArchive(h, "source", spec.Source)
	} else {
		hashArchive(h, "deployment", spec.Deployment)
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// hashArchive folds one archive into h, keyed on CONTENT IDENTITY rather than
// storage location.
//
// The distinction is load-bearing for URL archives: `fission fn update`
// re-uploads the source to a NEW storagesvc URL even when the code is
// byte-identical, so hashing the URL reports a content change on an update
// that changed nothing — which re-queues a build, whose re-stamp mints a
// spurious RFC-0025 version. The checksum is the content identity, so it wins
// whenever it is present; the URL is folded in only as a fallback for archives
// that carry no checksum, where a location change is the best signal available.
//
// Every field is length-prefixed so concatenation can never be ambiguous —
// without it, an image reference of "a" + digest "bc" would hash identically
// to "ab" + "c", and two genuinely different packages would look unchanged.
func hashArchive(h io.Writer, label string, a fv1.Archive) {
	write(h, label)
	write(h, string(a.Type))
	if a.OCI != nil {
		write(h, "oci")
		write(h, a.OCI.Image)
		write(h, a.OCI.Digest)
		// SubPath selects WHICH SUBTREE of the image becomes the deployment
		// root, so it changes the delivered bytes — unlike ImagePullSecrets,
		// which only changes how the same bytes are reached. Repointing a
		// package at a patched directory inside the same image (digest
		// unchanged) must converge, or running pods serve the unpatched
		// subtree forever while newly created pods serve the patched one.
		// Normalised the way the executor does, so cosmetic slash churn is
		// not a content change.
		write(h, strings.Trim(a.OCI.SubPath, "/"))
	}
	if a.Checksum.Sum != "" {
		write(h, "checksum")
		write(h, string(a.Checksum.Type))
		write(h, a.Checksum.Sum)
	} else {
		write(h, "url")
		write(h, a.URL)
	}
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
