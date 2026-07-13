// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package statestore

// Quota bounds one Scope. A zero field means "unlimited".
type Quota struct {
	// MaxKeys caps the number of live keys in the scope. Enforced by the scoped
	// wrapper on writes that create a new key.
	MaxKeys int64
	// MaxValueBytes caps the size of a single value. Enforced on every write.
	MaxValueBytes int64
	// MaxNamespaceBytes is a namespace-wide byte budget. It is reserved: the KV
	// interface lists only within a (namespace, owner, keyspace) scope, so a
	// namespace-wide byte total needs the cross-scope accountant that RFC-0023's
	// statesvc owns; the scoped wrapper here does not enforce it yet.
	MaxNamespaceBytes int64
}

// QuotaResolver returns the effective quota for a Scope. Consumers back this with
// the owning CR's spec (RFC-0023 StateConfig); the default is a static quota.
type QuotaResolver interface {
	Resolve(s Scope) Quota
}

// StaticQuota implements QuotaResolver with one Quota for every scope.
type StaticQuota Quota

// Resolve returns the static quota for any scope.
func (q StaticQuota) Resolve(Scope) Quota { return Quota(q) }
