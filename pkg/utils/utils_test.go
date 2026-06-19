// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

func TestUrlForFunction(t *testing.T) {
	t.Parallel()
	// "default" is metav1.NamespaceDefault: it is folded out of the path so the
	// URL matches the form the router actually registers; other namespaces are
	// kept. A hardcoded /fission-function/default/<name> would not resolve.
	tests := []struct {
		name, namespace, want string
	}{
		{"fn", "default", "/fission-function/fn"},
		{"fn", "ns1", "/fission-function/ns1/fn"},
		{"my-fn", "team-a", "/fission-function/team-a/my-fn"},
	}
	for _, tc := range tests {
		t.Run(tc.namespace, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, UrlForFunction(tc.name, tc.namespace))
		})
	}
}

func TestIsURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"http", "http://example.com", true},
		{"https", "https://example.com", true},
		{"file", "file://example.com", false},
		{"filename", "foobar.zip", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsURL(tt.url); got != tt.want {
				t.Errorf("IsURL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetChecksum(t *testing.T) {
	tests := []struct {
		name    string
		src     io.Reader
		want    *fv1.Checksum
		wantErr bool
	}{
		{
			name: "string case",
			src:  bytes.NewReader([]byte("foobar hello world")),
			want: &fv1.Checksum{
				Type: "sha256",
				Sum:  "99936be1902361c29745aef68bd818f5f08246fc695e2d6e4cc474daf79fed32",
			},
			wantErr: false,
		},
		{
			name:    "empty reader",
			src:     nil,
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetChecksum(tt.src)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetChecksum() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetChecksum() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetStringValueFromEnv(t *testing.T) {
	varName := "TEST_VAR"
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{
			name:    "empty string case",
			value:   "",
			want:    "",
			wantErr: true,
		},
		{
			name:    "string case",
			value:   "test string",
			want:    "test string",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv(varName, tt.value)
			got, err := GetStringValueFromEnv(varName)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetStringValueFromEnv() error = %v, wantErr %v, got %s", err, tt.wantErr, got)
				return
			}
		})
	}

}

func TestGetUIntValueFromEnv(t *testing.T) {
	varName := "TEST_VAR"
	tests := []struct {
		name    string
		value   string
		want    uint
		wantErr bool
	}{
		{
			name:    "empty string case",
			value:   "",
			want:    0,
			wantErr: true,
		},
		{
			name:    "string case",
			value:   "test string",
			want:    0,
			wantErr: true,
		},
		{
			name:    "not uint case",
			value:   "-100",
			want:    0,
			wantErr: true,
		},
		{
			name:    "uint case",
			value:   "7",
			want:    7,
			wantErr: false,
		}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv(varName, tt.value)
			got, err := GetUIntValueFromEnv(varName)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetUIntValueFromEnv() error = %v, wantErr %v, got %d", err, tt.wantErr, got)
				return
			}
		})
	}
}

func TestGetIntValueFromEnv(t *testing.T) {
	varName := "TEST_VAR"
	tests := []struct {
		name    string
		value   string
		want    int
		wantErr bool
	}{
		{
			name:    "empty string case",
			value:   "",
			want:    0,
			wantErr: true,
		},
		{
			name:    "string case",
			value:   "test string",
			want:    0,
			wantErr: true,
		},
		{
			name:    "not int case",
			value:   "-100",
			want:    -100,
			wantErr: false,
		},
		{
			name:    "int case",
			value:   "7",
			want:    7,
			wantErr: false,
		}}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv(varName, tt.value)
			got, err := GetIntValueFromEnv(varName)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetIntValueFromEnv() error = %v, wantErr %v, got %d", err, tt.wantErr, got)
				return
			}
		})
	}
}

func TestDownloadUrl_FileModeIs0600(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	dst := filepath.Join(dir, "out.bin")
	if err := DownloadUrl(context.Background(), srv.Client(), srv.URL, dst); err != nil {
		t.Fatalf("DownloadUrl: %v", err)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode: got %#o, want 0600", got)
	}
}

func TestDownloadUrl_OverwriteAllowed(t *testing.T) {
	// Ensures the refactor preserved os.Create's overwrite semantics —
	// re-downloading to the same path must succeed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("v2"))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	dst := filepath.Join(dir, "out.bin")
	if err := os.WriteFile(dst, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := DownloadUrl(context.Background(), srv.Client(), srv.URL, dst); err != nil {
		t.Fatalf("DownloadUrl on existing path: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "v2" {
		t.Fatalf("expected overwrite to v2, got %q (err=%v)", got, err)
	}
	// Mode must be tightened to 0o600 even when overwriting a pre-existing
	// file with a broader mode — fchmod after OpenFile closes that window.
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("Stat after overwrite: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode after overwrite: got %#o, want 0600", mode)
	}
}

func TestDownloadUrlToRoot_ConfinesToBase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	t.Cleanup(srv.Close)

	base := t.TempDir()

	// Happy path: writes under base with mode 0600.
	dst := filepath.Join(base, "out.bin")
	if err := DownloadUrlToRoot(context.Background(), srv.Client(), srv.URL, base, dst); err != nil {
		t.Fatalf("DownloadUrlToRoot: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "payload" {
		t.Fatalf("expected payload, got %q (err=%v)", got, err)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode: got %#o, want 0600", mode)
	}

	// A path escaping base is rejected and nothing is written outside base.
	sentinel := filepath.Join(filepath.Dir(base), "sentinel.bin")
	if err := os.WriteFile(sentinel, []byte("intact"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := DownloadUrlToRoot(context.Background(), srv.Client(), srv.URL, base, "../sentinel.bin"); err == nil {
		t.Fatal("expected error for path escaping base")
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "intact" {
		t.Fatalf("sentinel was modified: %q (err=%v)", got, err)
	}
}

func TestRootFileChecksum_ConfinesToBase(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(base, "data.bin"), []byte("checksum me"), 0o600))

	sum, err := RootFileChecksum(base, "data.bin")
	require.NoError(t, err)
	require.NotNil(t, sum)

	want, err := GetFileChecksum(filepath.Join(base, "data.bin"))
	require.NoError(t, err)
	assert.Equal(t, want.Sum, sum.Sum)

	// A path escaping base is rejected.
	_, err = RootFileChecksum(base, "../../etc/hostname")
	assert.Error(t, err)
}
