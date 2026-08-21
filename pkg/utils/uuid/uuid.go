// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

// Package uuid is the single place Fission mints UUIDs. It wraps the stdlib
// uuid package so the version stays pinned in one spot (see NewUUID) and so
// callers that want a types.UID do not each repeat the conversion.
package uuid

import (
	// stduuid is the Go 1.27 standard library package. It is aliased because
	// this package is itself named uuid, and an unaliased import would make
	// the identifier `uuid` mean something different inside this file than it
	// does to every caller.
	stduuid "uuid"

	"k8s.io/apimachinery/pkg/types"
)

// NewUUID returns a new random (version 4) UUID as a Kubernetes types.UID.
//
// NewV4 is called rather than New: the stdlib documents New as equivalent to
// NewV4 only "at this time", so pinning the version here keeps the format
// stable for the identifiers Fission persists in object names and labels.
func NewUUID() types.UID {
	return types.UID(stduuid.NewV4().String())
}

// NewString returns a new random (version 4) UUID in its canonical string
// form. See NewUUID for why the version is pinned.
func NewString() string {
	return stduuid.NewV4().String()
}
