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

// validateMountPaths applies the RFC-0030 §4 rules to a function's file
// projections: each MountPath must be relative and confined, and no two
// references may target the same directory.
//
// The duplicate rule is the part admission can actually enforce. The final path
// segment written is the referenced object's DATA KEY, and keys are mutable
// after admission, so a webhook cannot see them — two references sharing a
// directory can therefore have one object's later-added key silently overwrite
// a file the other wrote. Rejecting the shared directory removes the only half
// of that this layer can see; the fetcher declines to overwrite a file it did
// not write in the same specialization pass, which covers the rest.
func (spec FunctionSpec) validateMountPaths() error {
	var errs error

	// Secrets and ConfigMaps land under DIFFERENT roots, so a path may repeat
	// across the two kinds without colliding — track them separately.
	seenSecret := map[string]string{}
	seenConfig := map[string]string{}

	for _, s := range spec.Secrets {
		cleaned, err := CleanMountPath(s.MountPath)
		if err != nil {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "SecretReference.MountPath", s.MountPath, err.Error()))
			continue
		}
		if cleaned == "" {
			continue
		}
		if prev, dup := seenSecret[cleaned]; dup {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "SecretReference.MountPath", s.MountPath,
				fmt.Sprintf("mountPath is already used by secret %q; two secrets writing to one directory can overwrite each other's keys", prev)))
			continue
		}
		seenSecret[cleaned] = s.Name
	}

	for _, c := range spec.ConfigMaps {
		cleaned, err := CleanMountPath(c.MountPath)
		if err != nil {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "ConfigMapReference.MountPath", c.MountPath, err.Error()))
			continue
		}
		if cleaned == "" {
			continue
		}
		if prev, dup := seenConfig[cleaned]; dup {
			errs = errors.Join(errs, MakeValidationErr(ErrorInvalidValue, "ConfigMapReference.MountPath", c.MountPath,
				fmt.Sprintf("mountPath is already used by configmap %q; two configmaps writing to one directory can overwrite each other's keys", prev)))
			continue
		}
		seenConfig[cleaned] = c.Name
	}

	return errs
}
