// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// PushOptions tune PushDirectory. The zero value pushes anonymously over TLS.
type PushOptions struct {
	// Keychain authenticates the push; nil means anonymous plus the
	// process-default keychain.
	Keychain authn.Keychain
	// InsecureRegistries is a host (host[:port]) allowlist permitted to use
	// plain HTTP — same semantics as ExtractOptions.InsecureRegistries.
	InsecureRegistries []string
}

// PushDirectory publishes the contents of dir as a single-layer OCI image
// (RFC-0012 producer): empty base + one tar.gz layer of the directory at the
// image root, OCI media types. The artifact is deliberately a real image
// manifest — consumable both by kubelet image volumes (Path B) and by
// ExtractImage's flatten (Path A) by construction.
//
// The image is tagged with the short form of its own digest (a human
// affordance; consumption is digest-pinned), and the returned reference and
// digest are what the Package's OCI archive records.
//
// The layer is built deterministically (sorted walk, zeroed timestamps, no
// owner info), so rebuilding identical content yields an identical digest and
// the registry deduplicates the blob. Symlinks and special files are
// rejected: the Path A extractor refuses links (they are an escape avenue),
// so publishing them would produce an artifact that cannot be consumed.
func PushDirectory(ctx context.Context, dir, repo string, opts PushOptions) (imageRef, digest string, err error) {
	layerFile, err := os.CreateTemp("", "fission-oci-layer-*.tar.gz")
	if err != nil {
		return "", "", fmt.Errorf("creating layer temp file: %w", err)
	}
	defer func() {
		layerFile.Close()
		os.Remove(layerFile.Name())
	}()
	if err := tarGzDirectory(dir, layerFile); err != nil {
		return "", "", fmt.Errorf("packaging %q: %w", dir, err)
	}
	if err := layerFile.Sync(); err != nil {
		return "", "", fmt.Errorf("flushing layer temp file: %w", err)
	}

	layer, err := tarball.LayerFromFile(layerFile.Name(), tarball.WithMediaType(types.OCILayer))
	if err != nil {
		return "", "", fmt.Errorf("building layer: %w", err)
	}

	base := mutate.MediaType(empty.Image, types.OCIManifestSchema1)
	base = mutate.ConfigMediaType(base, types.OCIConfigJSON)
	img, err := mutate.AppendLayers(base, layer)
	if err != nil {
		return "", "", fmt.Errorf("assembling image: %w", err)
	}

	dgst, err := img.Digest()
	if err != nil {
		return "", "", fmt.Errorf("computing image digest: %w", err)
	}

	// Tag = short digest: cosmetic (consumption pins the digest), stable, and
	// collision-free per repository.
	tagged := fmt.Sprintf("%s:%s", repo, dgst.Hex[:12])
	ref, err := parseReference(tagged, opts.InsecureRegistries)
	if err != nil {
		return "", "", err
	}

	kc := opts.Keychain
	if kc == nil {
		kc = authn.DefaultKeychain
	}
	if err := remote.Write(ref, img, remote.WithContext(ctx), remote.WithAuthFromKeychain(kc)); err != nil {
		return "", "", fmt.Errorf("pushing %q: %w", tagged, err)
	}
	return tagged, dgst.String(), nil
}

// tarGzDirectory writes dir's contents (relative to dir) as a deterministic
// tar.gz stream: sorted order (fs.WalkDir guarantees lexical order), zeroed
// times, no user/group. Directories and regular files only.
func tarGzDirectory(dir string, w io.Writer) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	err := fs.WalkDir(os.DirFS(dir), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeDir,
				Name:     filepath.ToSlash(path) + "/",
				Mode:     int64(info.Mode().Perm()),
				ModTime:  time.Unix(0, 0),
			})
		case info.Mode().IsRegular():
			if err := tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeReg,
				Name:     filepath.ToSlash(path),
				Mode:     int64(info.Mode().Perm()),
				Size:     info.Size(),
				ModTime:  time.Unix(0, 0),
			}); err != nil {
				return err
			}
			f, err := os.Open(filepath.Join(dir, path))
			if err != nil {
				return err
			}
			_, cerr := io.Copy(tw, f)
			f.Close()
			return cerr
		default:
			// Symlinks (an escape avenue the Path A extractor refuses) and
			// special files cannot be part of a function package.
			return fmt.Errorf("entry %q is not a regular file or directory; refusing to publish", path)
		}
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
