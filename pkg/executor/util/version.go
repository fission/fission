// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// versionSuffixPattern matches the "-v<seq>" tail of a FunctionVersion's name
// (minted as "<fn>-v<sequence>" by versioning.Publish). Mirrors
// poolmgr/gp_service.go's own versionServiceSuffix so newdeploy and container
// derive the identical bounded suffix from the same
// fission.io/function-version label value that poolmgr uses for its Service
// name; poolmgr keeps its own private copy (a cross-executor-type import
// would be unusual coupling) — keep the two in sync if either changes.
var versionSuffixPattern = regexp.MustCompile(`-v[0-9]+$`)

// VersionSuffix derives the bounded suffix a per-version Kubernetes object
// name adds for a published FunctionVersion: the version label's own
// "-v<seq>" tail when it matches the expected shape, or a short
// deterministic hash-derived fallback otherwise — so a version label that
// (by bug or hand-edit) doesn't end in "-v<seq>" can never blow a name's
// 63-char budget open-ended. Either way the result is a handful of bytes,
// bounded independent of the label's own length for every input the
// fallback branch handles; the match branch is bounded in practice because
// versioning.Publish mints sequence numbers as a Go int64 (<=19 digits).
func VersionSuffix(versionLabel string) string {
	if m := versionSuffixPattern.FindString(versionLabel); m != "" {
		return m
	}
	h := sha256.Sum256([]byte(versionLabel))
	return "-v" + hex.EncodeToString(h[:])[:8]
}

// VersionedObjName returns the deterministic per-object name for a
// function's Kubernetes objects (Deployment/Service/HPA), shared by the
// newdeploy and container executor types' getObjName. It reproduces the
// legacy engineered budget — prefix (e.g. "newdeploy-"/"container-", 10
// chars including the trailing dash) + functionMetadata (<=35 chars) + "-"
// + uid (17 chars) landing at exactly 63 — and, for a versioned Function
// (fv1.FUNCTION_VERSION label present), shrinks the functionMetadata budget
// by len(VersionSuffix(...)) and appends that suffix, so the whole name
// still fits within the Kubernetes 63-char object-name limit. The
// unversioned path (no FUNCTION_VERSION label) is byte-identical to the
// pre-RFC-0025 behaviour: suffix=="" leaves functionMetadata's budget
// untouched.
func VersionedObjName(prefix string, fn *fv1.Function) string {
	uid := fn.UID[len(fn.UID)-17:]
	var functionMetadata string
	if len(fn.Name)+len(fn.Namespace) < 35 {
		functionMetadata = fn.Name + "-" + fn.Namespace
	} else {
		if len(fn.Name) > 17 {
			functionMetadata = fn.Name[:17]
		} else {
			functionMetadata = fn.Name
		}
		if len(fn.Namespace) > 17 {
			functionMetadata = functionMetadata + "-" + fn.Namespace[:17]
		} else {
			functionMetadata = functionMetadata + "-" + fn.Namespace
		}
	}

	suffix := ""
	if v := fn.Labels[fv1.FUNCTION_VERSION]; v != "" {
		suffix = VersionSuffix(v)
	}
	if suffix != "" {
		// Reserve room for the suffix: the unversioned budget (35 chars for
		// functionMetadata, engineered so prefix(10) + meta(35) + "-" +
		// uid(17) lands at exactly 63) shrinks by len(suffix) so
		// prefix + meta + "-" + uid + suffix still fits in 63.
		budget := 35 - len(suffix)
		if budget < 0 {
			budget = 0
		}
		if len(functionMetadata) > budget {
			functionMetadata = functionMetadata[:budget]
		}
	}

	// constructed name should be 63 characters long, as it is a valid k8s name
	// functionMetadata should be 35 characters long, as we take 17 characters from functionUid
	// with the 10-character prefix (unversioned); a versioned function further
	// truncates functionMetadata to reserve room for the "-v<seq>" suffix.
	return strings.ToLower(fmt.Sprintf("%s%s-%s%s", prefix, functionMetadata, uid, suffix))
}
