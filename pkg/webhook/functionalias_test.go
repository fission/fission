// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/generated/clientset/versioned/scheme"
)

func makeValidFunctionAlias() *v1.FunctionAlias {
	return &v1.FunctionAlias{
		ObjectMeta: metav1.ObjectMeta{Name: "alias-1", Namespace: "default"},
		Spec:       v1.FunctionAliasSpec{FunctionName: "fn", Version: "fn-v1"},
	}
}

func functionVersionFor(fnName, versionName string) *v1.FunctionVersion {
	return &v1.FunctionVersion{
		ObjectMeta: metav1.ObjectMeta{Name: versionName, Namespace: "default"},
		Spec: v1.FunctionVersionSpec{
			FunctionName:       fnName,
			FunctionUID:        "fn-uid",
			FunctionGeneration: 1,
			Sequence:           1,
			PackageDigest:      "sha256:" + versionSample64,
			PublishedAt:        metav1.Now(),
		},
	}
}

func TestFunctionAliasWebhook_Validate_SpecRules(t *testing.T) {
	r := &FunctionAlias{}

	if err := r.Validate(makeValidFunctionAlias()); err != nil {
		t.Fatalf("valid alias must be accepted (no reader wired): %v", err)
	}

	bad := makeValidFunctionAlias()
	bad.Spec.Version = ""
	err := r.Validate(bad)
	if err == nil || !strings.Contains(err.Error(), "exactly one of version or packageDigest") {
		t.Fatalf("expected XOR rejection, got: %v", err)
	}
}

// TestFunctionAliasWebhook_ReferenceIntegrity: spec.version and
// spec.secondaryVersion must name an existing FunctionVersion that belongs to
// the same function; a digest-pinned primary target is exempt from the
// lookup (eventual consistency).
func TestFunctionAliasWebhook_ReferenceIntegrity(t *testing.T) {
	t.Run("missing version rejected", func(t *testing.T) {
		r := &FunctionAlias{reader: fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()}
		err := r.Validate(makeValidFunctionAlias())
		if err == nil || !strings.Contains(err.Error(), "fn-v1") {
			t.Fatalf("expected missing-version rejection, got: %v", err)
		}
	})

	t.Run("version belonging to a different function rejected", func(t *testing.T) {
		v := functionVersionFor("other-fn", "fn-v1")
		r := &FunctionAlias{reader: fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(v).Build()}
		err := r.Validate(makeValidFunctionAlias())
		if err == nil || !strings.Contains(err.Error(), "other-fn") {
			t.Fatalf("expected wrong-function rejection, got: %v", err)
		}
	})

	t.Run("existing version for the right function accepted", func(t *testing.T) {
		v := functionVersionFor("fn", "fn-v1")
		r := &FunctionAlias{reader: fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(v).Build()}
		if err := r.Validate(makeValidFunctionAlias()); err != nil {
			t.Fatalf("unexpected rejection: %v", err)
		}
	})

	t.Run("digest-pinned alias exempt from lookup", func(t *testing.T) {
		a := &v1.FunctionAlias{
			ObjectMeta: metav1.ObjectMeta{Name: "alias-1", Namespace: "default"},
			Spec:       v1.FunctionAliasSpec{FunctionName: "fn", PackageDigest: "sha256:" + versionSample64},
		}
		r := &FunctionAlias{reader: fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()}
		if err := r.Validate(a); err != nil {
			t.Fatalf("digest-pinned alias must not require a version lookup: %v", err)
		}
	})

	t.Run("secondaryVersion missing rejected", func(t *testing.T) {
		primary := functionVersionFor("fn", "fn-v1")
		a := makeValidFunctionAlias()
		a.Spec.SecondaryVersion = "fn-v2"
		w := 50
		a.Spec.Weight = &w
		r := &FunctionAlias{reader: fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(primary).Build()}
		err := r.Validate(a)
		if err == nil || !strings.Contains(err.Error(), "fn-v2") {
			t.Fatalf("expected missing-secondaryVersion rejection, got: %v", err)
		}
	})

	t.Run("secondaryVersion belonging to a different function rejected", func(t *testing.T) {
		primary := functionVersionFor("fn", "fn-v1")
		secondary := functionVersionFor("other-fn", "fn-v2")
		a := makeValidFunctionAlias()
		a.Spec.SecondaryVersion = "fn-v2"
		w := 50
		a.Spec.Weight = &w
		r := &FunctionAlias{reader: fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(primary, secondary).Build()}
		err := r.Validate(a)
		if err == nil || !strings.Contains(err.Error(), "other-fn") {
			t.Fatalf("expected wrong-function rejection for secondaryVersion, got: %v", err)
		}
	})

	t.Run("secondaryVersion for the right function accepted", func(t *testing.T) {
		primary := functionVersionFor("fn", "fn-v1")
		secondary := functionVersionFor("fn", "fn-v2")
		a := makeValidFunctionAlias()
		a.Spec.SecondaryVersion = "fn-v2"
		w := 50
		a.Spec.Weight = &w
		r := &FunctionAlias{reader: fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(primary, secondary).Build()}
		if err := r.Validate(a); err != nil {
			t.Fatalf("unexpected rejection: %v", err)
		}
	})

	t.Run("no reader: reference checks skipped (fail open)", func(t *testing.T) {
		r := &FunctionAlias{}
		if err := r.Validate(makeValidFunctionAlias()); err != nil {
			t.Fatalf("nil reader must not block: %v", err)
		}
	})
}

// TestFunctionAliasWebhook_UpdatePath_DanglingVersionRejected exercises the
// actual UPDATE admission entrypoint (GenericWebhook.ValidateUpdate), not
// just Validate() directly: an alias that starts out pointing at an existing
// FunctionVersion must still be rejected when the update transitions
// spec.Version to a version name that does not exist. The create path is
// covered by TestFunctionAliasWebhook_ReferenceIntegrity; ValidateUpdate
// wiring (r.Validator set, delegating to the same Validate(new) the create
// path uses) was not previously exercised for an update that turns a
// previously-valid reference dangling.
func TestFunctionAliasWebhook_UpdatePath_DanglingVersionRejected(t *testing.T) {
	existing := functionVersionFor("fn", "fn-v1")
	r := &FunctionAlias{reader: fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(existing).Build()}
	r.Validator = r

	oldAlias := makeValidFunctionAlias() // spec.Version = "fn-v1" (exists)
	if err := r.Validate(oldAlias); err != nil {
		t.Fatalf("precondition: old alias must be valid: %v", err)
	}

	newAlias := oldAlias.DeepCopy()
	newAlias.Spec.Version = "fn-v-does-not-exist"

	_, err := r.ValidateUpdate(t.Context(), oldAlias, newAlias)
	if err == nil || !strings.Contains(err.Error(), "fn-v-does-not-exist") {
		t.Fatalf("expected update to a dangling version to be rejected via ValidateUpdate, got: %v", err)
	}
}
