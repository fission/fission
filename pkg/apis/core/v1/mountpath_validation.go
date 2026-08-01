// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// CleanMountPath normalises a MountPath and reports whether it is usable.
//
// The value is RELATIVE to the /secrets or /configs root, never absolute
// (RFC-0030 §4): generic pool pods share a fixed set of emptyDirs frozen at
// pool creation, so the fetcher physically cannot materialise a file at an
// arbitrary absolute path, and the container executor accepts the same
// constraint rather than diverging per executor.
//
// It is exported because the fetcher must derive exactly the same directory the
// webhook validated. Two implementations of "where does this go" would drift,
// and the drift would only show up as files written somewhere nobody looks.
func CleanMountPath(mountPath string) (string, error) {
	if mountPath == "" {
		return "", nil
	}
	if path.IsAbs(mountPath) {
		return "", fmt.Errorf("mountPath %q must be relative: it is resolved under the shared secrets/configs root, not the container filesystem root", mountPath)
	}
	if strings.ContainsRune(mountPath, '\\') {
		return "", fmt.Errorf("mountPath %q must not contain a backslash", mountPath)
	}

	cleaned := path.Clean(mountPath)
	// path.Clean turns "a/../.." into ".." and "." into "."; both escape or
	// collapse to the root, neither of which is a usable per-object directory.
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("mountPath %q must stay within the shared secrets/configs root", mountPath)
	}
	return cleaned, nil
}

// ObjectSubPath is the directory one referenced object's keys are written to,
// relative to the shared /secrets or /configs root: MountPath when set, the
// historical <namespace>/<name> otherwise.
//
// Exported so the fetcher derives exactly the directory admission validated.
// Two implementations of "where does this go" would drift, and the drift would
// surface only as files written somewhere nobody looks.
func ObjectSubPath(mountPath, namespace, name string) (string, error) {
	cleaned, err := CleanMountPath(mountPath)
	if err != nil {
		return "", err
	}
	if cleaned != "" {
		return cleaned, nil
	}
	return path.Join(namespace, name), nil
}

// validateMountPaths applies the RFC-0030 §4 rules to a function's file
// projections: each MountPath must be relative and confined, and no two
// references of the same kind may resolve to the same directory.
//
// The duplicate rule is the part admission can enforce. The final path segment
// written is the referenced object's DATA KEY, and keys are mutable after
// admission, so a webhook cannot see them — two references sharing a directory
// can have one object's later-added key overwrite a file the other wrote — the
// writer truncates, it does not refuse a pre-existing file.
//
// Collisions are computed on the RESOLVED directory, not on MountPath alone. A
// reference with no MountPath still occupies <namespace>/<name>, so an explicit
// MountPath of "default/creds" collides with a reference to secret "creds" in
// namespace "default" even though only one of the two sets the field. Checking
// explicit values against each other would miss exactly that case while
// claiming the directory is unshared.
func (spec FunctionSpec) validateMountPaths() error {
	var errs error

	// Secrets and ConfigMaps land under DIFFERENT roots, so a path may repeat
	// across the two kinds without colliding — track them separately.
	seenSecret := map[string]string{}
	seenConfig := map[string]string{}

	for _, s := range spec.Secrets {
		dir, err := ObjectSubPath(s.MountPath, s.Namespace, s.Name)
		if err != nil {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "SecretReference.MountPath", s.MountPath, err.Error()))
			continue
		}
		if prev, dup := seenSecret[dir]; dup {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "SecretReference.MountPath", s.MountPath,
				fmt.Sprintf("resolves to %q, already used by secret %q; two secrets writing to one directory can overwrite each other's keys", dir, prev)))
			continue
		}
		seenSecret[dir] = s.Name
	}

	for _, c := range spec.ConfigMaps {
		dir, err := ObjectSubPath(c.MountPath, c.Namespace, c.Name)
		if err != nil {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "ConfigMapReference.MountPath", c.MountPath, err.Error()))
			continue
		}
		if prev, dup := seenConfig[dir]; dup {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "ConfigMapReference.MountPath", c.MountPath,
				fmt.Sprintf("resolves to %q, already used by configmap %q; two configmaps writing to one directory can overwrite each other's keys", dir, prev)))
			continue
		}
		seenConfig[dir] = c.Name
	}

	return errs
}
