// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package oci pulls function code out of OCI images (RFC-0001). It is
// deliberately independent of the fetcher so that other consumers (newdeploy,
// future build/push tooling) can reuse it.
package oci

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// DefaultMaxBytes caps the total extracted size when ExtractOptions.MaxBytes
// is unset — a guard against decompression bombs.
const DefaultMaxBytes = int64(2) << 30 // 2 GiB

// ExtractOptions tune ExtractImage.
type ExtractOptions struct {
	// SubPath keeps only entries under this prefix inside the image
	// filesystem and re-roots them at the destination. "" or "/" means the
	// image root.
	SubPath string
	// Digest, when set ("sha256:<64 hex>"), must match the image's digest;
	// extraction fails before any write otherwise.
	Digest string
	// Keychain authenticates the pull; nil means anonymous plus the
	// process-default keychain.
	Keychain authn.Keychain
	// InsecureRegistries is a host (host[:port]) allowlist permitted to use
	// plain HTTP. Default empty: every registry must serve TLS.
	InsecureRegistries []string
	// MaxBytes caps the total extracted bytes; <= 0 applies DefaultMaxBytes.
	MaxBytes int64
}

// ExtractImage pulls imageRef, flattens its layers (whiteouts resolved), and
// extracts the filesystem into destDir under destRoot. Writes are confined to
// destRoot by os.Root — same threat model as utils.UnarchiveInRoot: entries
// that traverse out (absolute, ".."), symlinks, and hardlinks are refused,
// modes are masked to permission bits, and the total size is capped.
func ExtractImage(ctx context.Context, imageRef, destRoot, destDir string, opts ExtractOptions) error {
	ref, err := parseReference(imageRef, opts.InsecureRegistries)
	if err != nil {
		return err
	}

	kc := opts.Keychain
	if kc == nil {
		kc = authn.DefaultKeychain
	}
	img, err := remote.Image(ref, remote.WithContext(ctx), remote.WithAuthFromKeychain(kc))
	if err != nil {
		return fmt.Errorf("pulling image %q: %w", imageRef, err)
	}

	if opts.Digest != "" {
		actual, err := img.Digest()
		if err != nil {
			return fmt.Errorf("reading digest of image %q: %w", imageRef, err)
		}
		if actual.String() != opts.Digest {
			return fmt.Errorf("image %q digest mismatch: expected %s, got %s", imageRef, opts.Digest, actual.String())
		}
	}

	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}

	flattened := mutate.Extract(img)
	defer flattened.Close()

	if err := extractTar(flattened, destRoot, destDir, opts.SubPath, maxBytes); err != nil {
		return fmt.Errorf("extracting image %q: %w", imageRef, err)
	}
	return nil
}

// parseReference parses imageRef, allowing plain HTTP only for registries on
// the allowlist.
func parseReference(imageRef string, insecureRegistries []string) (name.Reference, error) {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return nil, fmt.Errorf("invalid image reference %q: %w", imageRef, err)
	}
	if slices.Contains(insecureRegistries, ref.Context().RegistryStr()) {
		ref, err = name.ParseReference(imageRef, name.Insecure)
		if err != nil {
			return nil, fmt.Errorf("invalid image reference %q: %w", imageRef, err)
		}
	}
	return ref, nil
}

// extractTar writes the flattened image tar stream into destDir under
// destRoot with the confinement rules described on ExtractImage.
func extractTar(r io.Reader, destRoot, destDir, subPath string, maxBytes int64) error {
	// The destination root must exist before a root can be opened on it.
	// Mode 0o755 so other containers in the pod (different UIDs) can read
	// the shared volume — same reasoning as utils.unarchiveStream.
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return fmt.Errorf("creating destination root: %w", err)
	}
	root, err := os.OpenRoot(destRoot)
	if err != nil {
		return fmt.Errorf("opening destination root: %w", err)
	}
	defer root.Close()

	if err := root.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	subPath = strings.Trim(subPath, "/")
	var written int64

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading image tar: %w", err)
		}

		entry := hdr.Name
		// Image layer tars conventionally use relative names, but nothing
		// stops a hand-built layer from using absolute or traversing ones.
		if strings.HasPrefix(entry, "/") || filepath.IsAbs(entry) {
			return fmt.Errorf("image entry %q is absolute; refusing to extract", hdr.Name)
		}
		clean := filepath.Clean(filepath.FromSlash(entry))
		if clean == "." {
			continue
		}
		if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("image entry %q escapes destination; refusing to extract", hdr.Name)
		}

		if subPath != "" {
			prefix := subPath + string(os.PathSeparator)
			if clean == subPath {
				continue // the sub-path directory itself; destDir already exists
			}
			if !strings.HasPrefix(clean, prefix) {
				continue // outside the requested sub-path
			}
			clean = strings.TrimPrefix(clean, prefix)
		}
		dest := filepath.Join(destDir, clean)

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(dest, 0o755); err != nil {
				return fmt.Errorf("creating directory %q: %w", clean, err)
			}
		case tar.TypeReg:
			if written+hdr.Size > maxBytes {
				return fmt.Errorf("image content exceeds the %d-byte extraction limit", maxBytes)
			}
			if dir := filepath.Dir(dest); dir != "." {
				if err := root.MkdirAll(dir, 0o755); err != nil {
					return fmt.Errorf("creating parent directory of %q: %w", clean, err)
				}
			}
			// Mode masked to permission bits at create time so a concurrent
			// observer never sees setuid/setgid/sticky or a wider mode.
			f, err := root.OpenFile(dest, os.O_RDWR|os.O_CREATE|os.O_TRUNC, hdr.FileInfo().Mode().Perm())
			if err != nil {
				return fmt.Errorf("creating file %q: %w", clean, err)
			}
			n, err := io.Copy(f, tr)
			closeErr := f.Close()
			if err != nil {
				return fmt.Errorf("writing file %q: %w", clean, err)
			}
			if closeErr != nil {
				return fmt.Errorf("closing file %q: %w", clean, closeErr)
			}
			written += n
			if written > maxBytes {
				return fmt.Errorf("image content exceeds the %d-byte extraction limit", maxBytes)
			}
		case tar.TypeSymlink, tar.TypeLink:
			// Function packages have no need for links, and they are an
			// avenue for escaping the extraction root.
			return fmt.Errorf("image entry %q is a link; refusing to extract", hdr.Name)
		default:
			// Devices, FIFOs, and other special entries are silently skipped:
			// they cannot hold function code.
			continue
		}
	}
}
