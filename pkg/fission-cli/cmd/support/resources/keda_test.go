// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package resources

import (
	"os"
	"path/filepath"
	"testing"

	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	kedafake "github.com/kedacore/keda/v2/pkg/generated/clientset/versioned/fake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func mqtOwnerRef() metav1.OwnerReference {
	return metav1.OwnerReference{Kind: mqtKind, Name: "my-mqt", APIVersion: "fission.io/v1"}
}

// dumpedFiles returns the base names written into dir.
func dumpedFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestKedaDumperDumpsOnlyMessageQueueTriggerOwnedObjects(t *testing.T) {
	t.Parallel()

	// Created by fission's mqt-keda scaler.
	ours := &kedav1alpha1.ScaledObject{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-mqt", Namespace: "default", ResourceVersion: "1",
			OwnerReferences: []metav1.OwnerReference{mqtOwnerRef()},
		},
	}
	// Someone else's ScaledObject: KEDA is cluster-wide, and a support bundle
	// must not carry an unrelated team's scaling config.
	theirs := &kedav1alpha1.ScaledObject{
		ObjectMeta: metav1.ObjectMeta{
			Name: "other-teams-app", Namespace: "other", ResourceVersion: "1",
			OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web"}},
		},
	}
	// No owner at all — hand-written by an operator, also not ours.
	orphan := &kedav1alpha1.ScaledObject{
		ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: "default", ResourceVersion: "1"},
	}

	dir := t.TempDir()
	d := KedaDumper{
		client:   kedafake.NewSimpleClientset(ours, theirs, orphan),
		kedaType: KedaScaledObject,
	}
	d.Dump(t.Context(), dir)

	files := dumpedFiles(t, dir)
	require.Len(t, files, 1, "only the MessageQueueTrigger-owned ScaledObject should be dumped")
	assert.Contains(t, files[0], "my-mqt")
}

func TestKedaDumperMasksVaultToken(t *testing.T) {
	t.Parallel()

	ta := &kedav1alpha1.TriggerAuthentication{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-mqt-auth", Namespace: "default", ResourceVersion: "1",
			OwnerReferences: []metav1.OwnerReference{mqtOwnerRef()},
		},
		Spec: kedav1alpha1.TriggerAuthenticationSpec{
			HashiCorpVault: &kedav1alpha1.HashiCorpVault{
				Address:    "https://vault.example.com",
				Credential: &kedav1alpha1.Credential{Token: "s.verysecrettoken"},
			},
		},
	}

	dir := t.TempDir()
	d := KedaDumper{
		client:   kedafake.NewSimpleClientset(ta),
		kedaType: KedaTriggerAuthentication,
	}
	d.Dump(t.Context(), dir)

	files := dumpedFiles(t, dir)
	require.Len(t, files, 1)

	content, err := os.ReadFile(filepath.Join(dir, files[0]))
	require.NoError(t, err)

	assert.NotContains(t, string(content), "s.verysecrettoken",
		"the vault token must never reach a support bundle")
	assert.Contains(t, string(content), "token: '-'",
		"the field should be masked, not dropped, so it is clear it was set")
}

func TestKedaDumperLeavesTheListedObjectUnmutated(t *testing.T) {
	t.Parallel()

	// Masking must not edit the object the API returned — triggerAuthClean
	// copies first because the token sits behind a shared pointer.
	ta := &kedav1alpha1.TriggerAuthentication{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-mqt-auth", Namespace: "default", ResourceVersion: "1",
			OwnerReferences: []metav1.OwnerReference{mqtOwnerRef()},
		},
		Spec: kedav1alpha1.TriggerAuthenticationSpec{
			HashiCorpVault: &kedav1alpha1.HashiCorpVault{
				Credential: &kedav1alpha1.Credential{Token: "s.verysecrettoken"},
			},
		},
	}

	cleaned := triggerAuthClean(ta)
	assert.Equal(t, "-", cleaned.Spec.HashiCorpVault.Credential.Token)
	assert.Equal(t, "s.verysecrettoken", ta.Spec.HashiCorpVault.Credential.Token,
		"the input object must not be modified in place")
}

func TestKedaDumperUnknownTypeWritesNothing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	d := KedaDumper{client: kedafake.NewSimpleClientset(), kedaType: "NotAKedaKind"}
	d.Dump(t.Context(), dir)

	assert.Empty(t, dumpedFiles(t, dir))
}

func TestKedaAbsent(t *testing.T) {
	t.Parallel()

	gvk := schema.GroupVersionKind{Group: "keda.sh", Version: "v1alpha1", Kind: "ScaledObject"}
	gr := schema.GroupResource{Group: "keda.sh", Resource: "scaledobjects"}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			// What a cluster without the KEDA CRDs actually returns.
			name: "no kind match means keda is not installed",
			err:  &apimeta.NoKindMatchError{GroupKind: gvk.GroupKind()},
			want: true,
		},
		{
			name: "not found means keda is not installed",
			err:  apierrors.NewNotFound(gr, "scaledobjects"),
			want: true,
		},
		{
			// Must stay a visible warning: this is a real problem worth seeing.
			name: "forbidden is a real error",
			err:  apierrors.NewForbidden(gr, "scaledobjects", assert.AnError),
			want: false,
		},
		{
			name: "arbitrary error is a real error",
			err:  assert.AnError,
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, kedaAbsent(tc.err))
		})
	}
}
