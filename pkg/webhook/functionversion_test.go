// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

// versionSample64 is a syntactically-valid 64-hex-char digest suffix, used
// wherever a test needs a well-formed PackageDigest but the value itself is
// not under test.
const versionSample64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func makeValidFunctionVersion() *v1.FunctionVersion {
	return &v1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "fn-v1", Namespace: "default"},
		Spec: v1.FunctionVersionSpec{
			FunctionName:       "fn",
			FunctionUID:        types.UID("fn-uid"),
			FunctionGeneration: 1,
			Sequence:           1,
			Snapshot:           v1.FunctionSpec{},
			PackageDigest:      "sha256:" + versionSample64,
			PublishedAt:        metav1.Now(),
		},
	}
}

func TestFunctionVersionWebhook_Validate(t *testing.T) {
	r := &FunctionVersion{}

	assert := func(t *testing.T, err error, wantErr bool, sub string) {
		t.Helper()
		if wantErr {
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if sub != "" && !strings.Contains(err.Error(), sub) {
				t.Fatalf("error %q does not contain %q", err, sub)
			}
			return
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	t.Run("valid version accepted", func(t *testing.T) {
		assert(t, r.Validate(makeValidFunctionVersion()), false, "")
	})

	t.Run("name/sequence mismatch rejected", func(t *testing.T) {
		fv := makeValidFunctionVersion()
		fv.Name = "fn-v2"
		assert(t, r.Validate(fv), true, "fn-v1")
	})

	t.Run("spec rule violation propagates from FunctionVersionSpec.Validate", func(t *testing.T) {
		fv := makeValidFunctionVersion()
		fv.Spec.PackageDigest = ""
		assert(t, r.Validate(fv), true, "PackageDigest")
	})
}

// TestFunctionVersionSpecImmutable is the immutability matrix: every field of
// FunctionVersionSpec must be pinned after creation (a published version is a
// content-addressed snapshot), while metadata/label churn stays mutable.
func TestFunctionVersionSpecImmutable(t *testing.T) {
	r := &FunctionVersion{}
	old := makeValidFunctionVersion()

	t.Run("metadata/labels stay mutable", func(t *testing.T) {
		same := old.DeepCopy()
		same.Labels = map[string]string{"foo": "bar"}
		same.Annotations = map[string]string{"baz": "qux"}
		if err := r.ValidateTransition(old, same); err != nil {
			t.Fatalf("metadata-only change must be allowed: %v", err)
		}
	})

	cases := []struct {
		name   string
		mutate func(*v1.FunctionVersion)
	}{
		{"FunctionName", func(fv *v1.FunctionVersion) { fv.Spec.FunctionName = "other" }},
		{"FunctionUID", func(fv *v1.FunctionVersion) { fv.Spec.FunctionUID = "other-uid" }},
		{"FunctionGeneration", func(fv *v1.FunctionVersion) { fv.Spec.FunctionGeneration = 2 }},
		{"Sequence", func(fv *v1.FunctionVersion) { fv.Spec.Sequence = 2 }},
		{"PackageDigest", func(fv *v1.FunctionVersion) {
			fv.Spec.PackageDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
		}},
		{"Snapshot", func(fv *v1.FunctionVersion) { fv.Spec.Snapshot.InvokeStrategy.StrategyType = v1.StrategyTypeExecution }},
		{"PublishedAt", func(fv *v1.FunctionVersion) { fv.Spec.PublishedAt = metav1.NewTime(fv.Spec.PublishedAt.Add(1)) }},
	}
	for _, tc := range cases {
		t.Run(tc.name+" is immutable", func(t *testing.T) {
			changed := old.DeepCopy()
			tc.mutate(changed)
			err := r.ValidateTransition(old, changed)
			if err == nil {
				t.Fatalf("expected rejection for mutated %s, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), "immutable") {
				t.Fatalf("error must say immutable, got: %v", err)
			}
		})
	}
}

// TestFunctionVersionDeleteGuard is the delete-guard matrix: referenced via
// spec.Version / spec.SecondaryVersion / status.ResolvedVersion is rejected;
// unreferenced is allowed; the cascade-delete escape (owning Function absent
// or terminating) always allows.
func TestFunctionVersionDeleteGuard(t *testing.T) {
	fv := makeValidFunctionVersion() // name "fn-v1", namespace "default"

	aliasReferencing := func(field string) *v1.FunctionAlias {
		a := &v1.FunctionAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "alias-1", Namespace: "default"},
			Spec:       v1.FunctionAliasSpec{FunctionName: "fn"},
		}
		switch field {
		case "version":
			a.Spec.Version = "fn-v1"
		case "secondaryVersion":
			a.Spec.Version = "fn-v9"
			a.Spec.SecondaryVersion = "fn-v1"
			w := 50
			a.Spec.Weight = &w
		case "resolvedVersion":
			a.Spec.PackageDigest = "sha256:" + versionSample64
			a.Status.ResolvedVersion = "fn-v1"
		}
		return a
	}

	t.Run("referenced via spec.Version rejected", func(t *testing.T) {
		r := &FunctionVersion{reader: fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(aliasReferencing("version")).Build()}
		err := r.ValidateDeletion(t.Context(), fv)
		if err == nil || !strings.Contains(err.Error(), "alias-1") {
			t.Fatalf("expected rejection referencing alias-1, got: %v", err)
		}
	})

	t.Run("referenced via spec.SecondaryVersion rejected", func(t *testing.T) {
		r := &FunctionVersion{reader: fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(aliasReferencing("secondaryVersion")).Build()}
		err := r.ValidateDeletion(t.Context(), fv)
		if err == nil {
			t.Fatalf("expected rejection, got nil")
		}
	})

	t.Run("referenced via status.ResolvedVersion rejected", func(t *testing.T) {
		r := &FunctionVersion{reader: fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(aliasReferencing("resolvedVersion")).Build()}
		err := r.ValidateDeletion(t.Context(), fv)
		if err == nil {
			t.Fatalf("expected rejection, got nil")
		}
	})

	t.Run("unreferenced allowed", func(t *testing.T) {
		unrelated := &v1.FunctionAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "alias-2", Namespace: "default"},
			Spec:       v1.FunctionAliasSpec{FunctionName: "fn", Version: "fn-v2"},
		}
		r := &FunctionVersion{reader: fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(unrelated).Build()}
		if err := r.ValidateDeletion(t.Context(), fv); err != nil {
			t.Fatalf("unreferenced version must be deletable: %v", err)
		}
	})

	t.Run("no reader: allowed (webhook not wired for reads)", func(t *testing.T) {
		r := &FunctionVersion{}
		if err := r.ValidateDeletion(t.Context(), fv); err != nil {
			t.Fatalf("nil reader must not block: %v", err)
		}
	})

	t.Run("different namespace alias does not block", func(t *testing.T) {
		other := &v1.FunctionAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "alias-1", Namespace: "other-ns"},
			Spec:       v1.FunctionAliasSpec{FunctionName: "fn", Version: "fn-v1"},
		}
		r := &FunctionVersion{reader: fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(other).Build()}
		if err := r.ValidateDeletion(t.Context(), fv); err != nil {
			t.Fatalf("cross-namespace alias must not block delete: %v", err)
		}
	})

	fvWithOwner := func(ownerName string) *v1.FunctionVersion {
		v := makeValidFunctionVersion()
		v.OwnerReferences = []metav1.OwnerReference{{Kind: "Function", Name: ownerName, APIVersion: "fission.io/v1"}}
		return v
	}

	t.Run("cascade escape: owning Function absent, referenced version still deletable", func(t *testing.T) {
		v := fvWithOwner("fn")
		r := &FunctionVersion{reader: fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(aliasReferencing("version")).Build()}
		if err := r.ValidateDeletion(t.Context(), v); err != nil {
			t.Fatalf("owner-absent cascade escape must allow delete: %v", err)
		}
	})

	t.Run("cascade escape: owning Function terminating, referenced version still deletable", func(t *testing.T) {
		v := fvWithOwner("fn")
		fn := &v1.Function{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "fn",
				Namespace:         "default",
				DeletionTimestamp: &metav1.Time{Time: metav1.Now().Time},
				Finalizers:        []string{"keep-around-for-test"},
			},
		}
		r := &FunctionVersion{reader: fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(fn, aliasReferencing("version")).Build()}
		if err := r.ValidateDeletion(t.Context(), v); err != nil {
			t.Fatalf("owner-terminating cascade escape must allow delete: %v", err)
		}
	})

	t.Run("owning Function present and live, referenced version rejected", func(t *testing.T) {
		v := fvWithOwner("fn")
		fn := &v1.Function{ObjectMeta: metav1.ObjectMeta{Name: "fn", Namespace: "default"}}
		r := &FunctionVersion{reader: fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(fn, aliasReferencing("version")).Build()}
		err := r.ValidateDeletion(t.Context(), v)
		if err == nil {
			t.Fatalf("live owning Function must not escape the guard")
		}
	})
}
