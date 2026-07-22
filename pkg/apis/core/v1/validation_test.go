// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gkampitakis/go-snaps/snaps"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestAggregateValidationErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		errs []error
	}{
		{
			name: "no errors",
			errs: []error{},
		},
		{
			name: "one error",
			errs: []error{
				fmt.Errorf("E1"),
			},
		},
		{
			name: "multiple errors",
			errs: []error{
				fmt.Errorf("E1"),
				fmt.Errorf("E2"),
				fmt.Errorf("E3"),
			},
		},
		{
			name: "nested errors",
			errs: []error{
				fmt.Errorf("E1"),
				errors.Join(
					fmt.Errorf("E2"),
					errors.Join(
						fmt.Errorf("E3"),
						fmt.Errorf("E4"),
					),
				),
				fmt.Errorf("E5"),
				errors.Join(
					fmt.Errorf("E6"),
					fmt.Errorf("E7"),
				),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := errors.Join(tc.errs...)
			aggErr := AggregateValidationErrors("Environment", errs)
			snaps.MatchSnapshot(t, fmt.Sprint(aggErr))
		})
	}

	t.Run("nil error", func(t *testing.T) {
		aggErr := AggregateValidationErrors("Environment", nil)
		snaps.MatchSnapshot(t, fmt.Sprint(aggErr))
	})

	t.Run("simple error", func(t *testing.T) {
		aggErr := AggregateValidationErrors("Environment", fmt.Errorf("simple error"))
		snaps.MatchSnapshot(t, fmt.Sprint(aggErr))
	})
}

func TestHTTPTriggerCorsConfig_Validate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     *HTTPTriggerCorsConfig
		wantErr bool
		errSub  string
	}{
		{
			name: "nil receiver is no-op",
			cfg:  nil,
		},
		{
			name: "valid exact-origin allowlist",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins: []string{"https://app.example.com"},
				AllowMethods: []string{"GET", "POST"},
			},
		},
		{
			name: "valid wildcard without credentials",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins: []string{"*"},
			},
		},
		{
			name: "wildcard with credentials rejected",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins:     []string{"*"},
				AllowCredentials: true,
			},
			wantErr: true,
			errSub:  "AllowCredentials=true",
		},
		{
			name: "missing scheme rejected",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins: []string{"app.example.com"},
			},
			wantErr: true,
			errSub:  "scheme and host",
		},
		{
			name: "origin with path rejected",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins: []string{"https://app.example.com/api"},
			},
			wantErr: true,
			errSub:  "path, query, fragment",
		},
		{
			name: "origin with query rejected",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins: []string{"https://app.example.com/?x=1"},
			},
			wantErr: true,
			errSub:  "path, query, fragment",
		},
		{
			name: "malformed MaxAge rejected",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins: []string{"https://app.example.com"},
				MaxAge:       "garbage",
			},
			wantErr: true,
			errSub:  "time.Duration",
		},
		{
			name: "negative MaxAge rejected",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins: []string{"https://app.example.com"},
				MaxAge:       "-5m",
			},
			wantErr: true,
			errSub:  "non-negative",
		},
		{
			name: "valid MaxAge accepted",
			cfg: &HTTPTriggerCorsConfig{
				AllowOrigins: []string{"https://app.example.com"},
				MaxAge:       "10m",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errSub)
				}
				if tc.errSub != "" && !strings.Contains(err.Error(), tc.errSub) {
					t.Fatalf("error %q does not contain %q", err, tc.errSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// versionSample64 duplicates the same-purpose sample64 in
// functionversion_types_test.go (both are genuinely 64-hex-char digest
// suffixes). The duplication is necessary, not accidental: that file lives in
// package v1_test (external test package), so its unexported const is not
// visible from this file's package v1 (internal test package) regardless of
// value.
const versionSample64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // 64 hex chars, see comment above

func TestVersioningConfigValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     VersioningConfig
		wantErr bool
		errSub  string
	}{
		{name: "empty mode accepted (defaults to auto)"},
		{name: "auto mode accepted", cfg: VersioningConfig{Mode: VersioningModeAuto}},
		{name: "manual mode accepted", cfg: VersioningConfig{Mode: VersioningModeManual}},
		{name: "unknown mode rejected", cfg: VersioningConfig{Mode: "sometimes"}, wantErr: true, errSub: "not a valid versioning mode"},
		{name: "nil retain accepted"},
		{name: "retain 1 accepted", cfg: VersioningConfig{Retain: new(1)}},
		{name: "retain 0 rejected", cfg: VersioningConfig{Retain: new(0)}, wantErr: true, errSub: "must be >= 1"},
		{name: "negative retain rejected", cfg: VersioningConfig{Retain: new(-1)}, wantErr: true, errSub: "must be >= 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errSub)
				}
				if tc.errSub != "" && !strings.Contains(err.Error(), tc.errSub) {
					t.Fatalf("error %q does not contain %q", err, tc.errSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestFunctionVersionSpecValidate(t *testing.T) {
	validSpec := func() FunctionVersionSpec {
		return FunctionVersionSpec{
			FunctionName:       "fn",
			FunctionUID:        types.UID("fn-uid"),
			FunctionGeneration: 1,
			Sequence:           1,
			Snapshot:           FunctionSpec{},
			PackageDigest:      "sha256:" + versionSample64,
			PublishedAt:        metav1.Now(),
		}
	}

	for _, tc := range []struct {
		name    string
		mutate  func(*FunctionVersionSpec)
		wantErr bool
		errSub  string
	}{
		{name: "valid spec accepted", mutate: func(*FunctionVersionSpec) {}},
		{
			name:    "empty FunctionName rejected",
			mutate:  func(s *FunctionVersionSpec) { s.FunctionName = "" },
			wantErr: true,
		},
		{
			name:    "invalid FunctionName rejected",
			mutate:  func(s *FunctionVersionSpec) { s.FunctionName = "Not_A_Kube_Name" },
			wantErr: true,
		},
		{
			name:    "empty FunctionUID rejected",
			mutate:  func(s *FunctionVersionSpec) { s.FunctionUID = "" },
			wantErr: true,
			errSub:  "FunctionUID",
		},
		{
			name:    "FunctionGeneration 0 rejected",
			mutate:  func(s *FunctionVersionSpec) { s.FunctionGeneration = 0 },
			wantErr: true,
			errSub:  "FunctionGeneration",
		},
		{
			name:    "FunctionGeneration negative rejected",
			mutate:  func(s *FunctionVersionSpec) { s.FunctionGeneration = -1 },
			wantErr: true,
			errSub:  "FunctionGeneration",
		},
		{
			name:    "Sequence 0 rejected",
			mutate:  func(s *FunctionVersionSpec) { s.Sequence = 0 },
			wantErr: true,
			errSub:  "Sequence",
		},
		{
			name:    "empty PackageDigest rejected",
			mutate:  func(s *FunctionVersionSpec) { s.PackageDigest = "" },
			wantErr: true,
			errSub:  "PackageDigest",
		},
		{
			name: "Snapshot.Versioning non-nil rejected",
			mutate: func(s *FunctionVersionSpec) {
				s.Snapshot.Versioning = &VersioningConfig{Mode: VersioningModeAuto}
			},
			wantErr: true,
			errSub:  "snapshot must zero versioning to avoid recursion",
		},
		{
			name: "invalid Snapshot propagates FunctionSpec.Validate errors",
			mutate: func(s *FunctionVersionSpec) {
				s.Snapshot.InvokeStrategy = InvokeStrategy{StrategyType: "bogus"}
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := validSpec()
			tc.mutate(&spec)
			err := spec.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errSub)
				}
				if tc.errSub != "" && !strings.Contains(err.Error(), tc.errSub) {
					t.Fatalf("error %q does not contain %q", err, tc.errSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestFunctionVersionValidate(t *testing.T) {
	validVersion := func() FunctionVersion {
		return FunctionVersion{
			ObjectMeta: metav1.ObjectMeta{Name: "fn-v1", Namespace: "default"},
			Spec: FunctionVersionSpec{
				FunctionName:       "fn",
				FunctionUID:        types.UID("fn-uid"),
				FunctionGeneration: 1,
				Sequence:           1,
				Snapshot:           FunctionSpec{},
				PackageDigest:      "sha256:" + versionSample64,
				PublishedAt:        metav1.Now(),
			},
		}
	}

	t.Run("valid name accepted", func(t *testing.T) {
		fv := validVersion()
		if err := fv.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("mismatched name rejected", func(t *testing.T) {
		fv := validVersion()
		fv.Name = "fn-version-1"
		err := fv.Validate()
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "fn-v1") {
			t.Fatalf("error %q does not mention expected name", err)
		}
	})

	t.Run("wrong sequence in name rejected", func(t *testing.T) {
		fv := validVersion()
		fv.Spec.Sequence = 2
		err := fv.Validate()
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestFunctionAliasSpecValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		spec    FunctionAliasSpec
		wantErr bool
		errSub  string
	}{
		{
			name: "version-pinned accepted",
			spec: FunctionAliasSpec{FunctionName: "fn", Version: "fn-v1"},
		},
		{
			name: "digest-pinned accepted",
			spec: FunctionAliasSpec{FunctionName: "fn", PackageDigest: "sha256:" + versionSample64},
		},
		{
			name:    "invalid FunctionName rejected",
			spec:    FunctionAliasSpec{FunctionName: "Not_Valid", Version: "fn-v1"},
			wantErr: true,
		},
		{
			name:    "neither version nor digest rejected",
			spec:    FunctionAliasSpec{FunctionName: "fn"},
			wantErr: true,
			errSub:  "exactly one of version or packageDigest",
		},
		{
			name:    "both version and digest rejected",
			spec:    FunctionAliasSpec{FunctionName: "fn", Version: "fn-v1", PackageDigest: "sha256:" + versionSample64},
			wantErr: true,
			errSub:  "only one of version or packageDigest",
		},
		{
			name:    "invalid Version kube name rejected",
			spec:    FunctionAliasSpec{FunctionName: "fn", Version: "Not_Valid"},
			wantErr: true,
		},
		{
			name:    "malformed PackageDigest rejected",
			spec:    FunctionAliasSpec{FunctionName: "fn", PackageDigest: "sha256:short"},
			wantErr: true,
			errSub:  "64 hex characters",
		},
		{
			name: "weight with secondary accepted",
			spec: FunctionAliasSpec{
				FunctionName:     "fn",
				Version:          "fn-v1",
				Weight:           new(50),
				SecondaryVersion: "fn-v2",
			},
		},
		{
			name: "weight without secondary rejected",
			spec: FunctionAliasSpec{
				FunctionName: "fn",
				Version:      "fn-v1",
				Weight:       new(50),
			},
			wantErr: true,
			errSub:  "weight requires secondaryVersion",
		},
		{
			name: "weight out of range rejected",
			spec: FunctionAliasSpec{
				FunctionName:     "fn",
				Version:          "fn-v1",
				Weight:           new(101),
				SecondaryVersion: "fn-v2",
			},
			wantErr: true,
			errSub:  "must be 0-100",
		},
		{
			name: "negative weight rejected",
			spec: FunctionAliasSpec{
				FunctionName:     "fn",
				Version:          "fn-v1",
				Weight:           new(-1),
				SecondaryVersion: "fn-v2",
			},
			wantErr: true,
			errSub:  "must be 0-100",
		},
		{
			name: "secondary equal to version rejected",
			spec: FunctionAliasSpec{
				FunctionName:     "fn",
				Version:          "fn-v1",
				Weight:           new(50),
				SecondaryVersion: "fn-v1",
			},
			wantErr: true,
			errSub:  "must differ from version",
		},
		{
			name: "invalid SecondaryVersion kube name rejected",
			spec: FunctionAliasSpec{
				FunctionName:     "fn",
				Version:          "fn-v1",
				Weight:           new(50),
				SecondaryVersion: "Not_Valid",
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errSub)
				}
				if tc.errSub != "" && !strings.Contains(err.Error(), tc.errSub) {
					t.Fatalf("error %q does not contain %q", err, tc.errSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestFunctionAliasValidate(t *testing.T) {
	t.Run("valid alias accepted", func(t *testing.T) {
		fa := FunctionAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "fn-live", Namespace: "default"},
			Spec:       FunctionAliasSpec{FunctionName: "fn", Version: "fn-v1"},
		}
		if err := fa.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid metadata name rejected", func(t *testing.T) {
		fa := FunctionAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "Not_Valid", Namespace: "default"},
			Spec:       FunctionAliasSpec{FunctionName: "fn", Version: "fn-v1"},
		}
		if err := fa.Validate(); err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("invalid spec rejected", func(t *testing.T) {
		fa := FunctionAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "fn-live", Namespace: "default"},
			Spec:       FunctionAliasSpec{FunctionName: "fn"},
		}
		if err := fa.Validate(); err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestFunctionSpecValidateVersioning(t *testing.T) {
	t.Run("nil versioning accepted", func(t *testing.T) {
		spec := FunctionSpec{}
		if err := spec.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid versioning mode propagates", func(t *testing.T) {
		spec := FunctionSpec{Versioning: &VersioningConfig{Mode: "bogus"}}
		err := spec.Validate()
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "not a valid versioning mode") {
			t.Fatalf("error %q does not mention versioning mode", err)
		}
	})
}
