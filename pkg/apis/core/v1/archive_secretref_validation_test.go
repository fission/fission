// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	apiv1 "k8s.io/api/core/v1"
)

func TestArchiveSecretRefValidation(t *testing.T) {
	t.Parallel()
	ref := &apiv1.LocalObjectReference{Name: "artifactory-creds"}
	tests := []struct {
		name    string
		archive Archive
		wantErr string // empty = valid
	}{
		{"url with secretref", Archive{Type: ArchiveTypeUrl, URL: "https://repo.example.com/a.zip", SecretRef: ref}, ""},
		{"empty type with url and secretref", Archive{URL: "https://repo.example.com/a.zip", SecretRef: ref}, ""},
		{"secretref without url", Archive{Type: ArchiveTypeLiteral, Literal: []byte("x"), SecretRef: ref}, "secretref requires a url archive"},
		{"secretref with empty archive", Archive{SecretRef: ref}, "secretref requires a url archive"},
		{"secretref with oci", Archive{Type: ArchiveTypeOCI, OCI: &OCIArchive{Image: "r/i:t"}, SecretRef: ref}, "secretref requires a url archive"},
		{"secretref with storagesvc url", Archive{Type: ArchiveTypeUrl, URL: "http://storagesvc.fission/v1/archive?id=abc", SecretRef: ref}, "internal storage service"},
		{"secretref with bad name", Archive{Type: ArchiveTypeUrl, URL: "https://repo.example.com/a.zip", SecretRef: &apiv1.LocalObjectReference{Name: "Not_Valid!"}}, "SecretRef"},
		{"url without secretref still valid", Archive{Type: ArchiveTypeUrl, URL: "https://repo.example.com/a.zip"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.archive.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

// TestPackageValidateForAdmissionSecretRef pins that the SecretRef rules run
// at ADMISSION, not only on the CLI path: Package.ValidateForAdmission was a
// no-op before RFC-0031, and the storagesvc exclusion (never mix internal
// HMAC with external credentials) must hold for hand-authored objects that
// never passed through the CLI.
func TestPackageValidateForAdmissionSecretRef(t *testing.T) {
	t.Parallel()
	ref := &apiv1.LocalObjectReference{Name: "creds"}

	bad := &Package{}
	bad.Spec.Deployment = Archive{Type: ArchiveTypeUrl, URL: "http://storagesvc.fission/v1/archive?id=abc", SecretRef: ref}
	assert.ErrorContains(t, bad.ValidateForAdmission(), "internal storage service")

	badSrc := &Package{}
	badSrc.Spec.Source = Archive{Type: ArchiveTypeLiteral, Literal: []byte("x"), SecretRef: ref}
	assert.ErrorContains(t, badSrc.ValidateForAdmission(), "secretref requires a url archive")

	good := &Package{}
	good.Spec.Deployment = Archive{Type: ArchiveTypeUrl, URL: "https://repo.example.com/a.zip", SecretRef: ref}
	assert.NoError(t, good.ValidateForAdmission())

	plain := &Package{}
	plain.Spec.Deployment = Archive{Type: ArchiveTypeUrl, URL: "https://repo.example.com/a.zip"}
	assert.NoError(t, plain.ValidateForAdmission())
}

func TestIsStorageServiceURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url  string
		want bool
	}{
		{"http://storagesvc.fission/v1/archive?id=abc", true},
		{"https://storage.fission.svc:8000/v1/archive", true},
		{"https://repo.example.com/repo/archive.zip", false},
		{"https://repo.example.com/v1/archive/app.zip", false}, // external server, longer path — must NOT match
		{"https://repo.example.com/v1/archivex", false},        // exact-match: no false positive
		{"https://repo.example.com/v2/archive", false},
		{"://bad-url", false}, // unparseable → treated as external
		{"", false},
	}
	for _, tt := range tests {
		assert.Equalf(t, tt.want, IsStorageServiceURL(tt.url), "url=%q", tt.url)
	}
}
