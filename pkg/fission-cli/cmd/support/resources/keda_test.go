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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"

	"github.com/fission/fission/pkg/fission-cli/cmd"
	"github.com/fission/fission/pkg/mqtrigger"
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
		Name: "my-mqt", Namespace: "default", ResourceVersion: "1",
		OwnerReferences: []metav1.OwnerReference{mqtOwnerRef()},
	}
	// Someone else's ScaledObject: KEDA is cluster-wide, and a support bundle
	// must not carry an unrelated team's scaling config.
	theirs := &kedav1alpha1.ScaledObject{
		Name: "other-teams-app", Namespace: "other", ResourceVersion: "1",
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web"}},
	}
	// No owner at all — hand-written by an operator, also not ours.
	orphan := &kedav1alpha1.ScaledObject{
		Name: "orphan", Namespace: "default", ResourceVersion: "1",
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
		Name: "my-mqt-auth", Namespace: "default", ResourceVersion: "1",
		OwnerReferences: []metav1.OwnerReference{mqtOwnerRef()},
		Spec: kedav1alpha1.TriggerAuthenticationSpec{
			HashiCorpVault: &kedav1alpha1.HashiCorpVault{
				Address:    "https://vault.example.com",
				Credential: &kedav1alpha1.Credential{Token: "s.verysecrettoken"},
			},
		},
	}

	// Not ours. The ownership filter is what keeps another team's Vault address
	// and secret names out of a bundle that gets sent to a third party, and it
	// is applied on this branch too — not only to ScaledObjects.
	theirs := &kedav1alpha1.TriggerAuthentication{
		Name: "other-teams-auth", Namespace: "other", ResourceVersion: "1",
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web"}},
		Spec: kedav1alpha1.TriggerAuthenticationSpec{
			HashiCorpVault: &kedav1alpha1.HashiCorpVault{Address: "https://vault.other.example.com"},
		},
	}

	dir := t.TempDir()
	d := KedaDumper{
		client:   kedafake.NewSimpleClientset(ta, theirs),
		kedaType: KedaTriggerAuthentication,
	}
	d.Dump(t.Context(), dir)

	files := dumpedFiles(t, dir)
	require.Len(t, files, 1, "only the MessageQueueTrigger-owned TriggerAuthentication should be dumped")
	assert.Contains(t, files[0], "my-mqt-auth")

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
		Name: "my-mqt-auth", Namespace: "default", ResourceVersion: "1",
		OwnerReferences: []metav1.OwnerReference{mqtOwnerRef()},
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

	// Seed both kinds, owned, so the assertion discriminates: with an empty
	// clientset "wrote nothing" would hold however the default branch behaved.
	dir := t.TempDir()
	d := KedaDumper{
		client: kedafake.NewSimpleClientset(
			&kedav1alpha1.ScaledObject{
				Name: "my-mqt", Namespace: "default", ResourceVersion: "1",
				OwnerReferences: []metav1.OwnerReference{mqtOwnerRef()}},
			&kedav1alpha1.TriggerAuthentication{
				Name: "my-mqt-auth", Namespace: "default", ResourceVersion: "1",
				OwnerReferences: []metav1.OwnerReference{mqtOwnerRef()}},
		),
		kedaType: "NotAKedaKind",
	}
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

func TestMqtOwnerConstantsMatchScaler(t *testing.T) {
	t.Parallel()

	// The constants are copied rather than imported so this package does not
	// pull the scaler machinery into its build. This test is what makes that
	// trade safe: it fails the moment the originals move.
	assert.Equal(t, mqtrigger.MqtKind, mqtKind)
	assert.Equal(t, mqtrigger.MqtAPIVersion, mqtAPIVersion)
}

func TestKedaDumperIgnoresSameKindFromAnotherGroup(t *testing.T) {
	t.Parallel()

	// "MessageQueueTrigger" is not a Kind fission owns across every API group,
	// and this filter decides what reaches a third party.
	impostor := &kedav1alpha1.ScaledObject{
		Name: "someone-elses-mqt", Namespace: "other", ResourceVersion: "1",
		OwnerReferences: []metav1.OwnerReference{
			{Kind: mqtKind, Name: "their-mqt", APIVersion: "queues.example.com/v1"},
		},
	}

	dir := t.TempDir()
	d := KedaDumper{client: kedafake.NewSimpleClientset(impostor), kedaType: KedaScaledObject}
	d.Dump(t.Context(), dir)

	assert.Empty(t, dumpedFiles(t, dir),
		"a same-named Kind from another API group is not fission's")
}

func TestKedaDumperWithUnusableClientWritesNothing(t *testing.T) {
	t.Parallel()

	// The deferred-error design exists so a bad RestConfig cannot abort the
	// whole bundle. Without the guard this dereferences a nil client.
	dir := t.TempDir()
	d := KedaDumper{client: nil, kedaType: KedaScaledObject, clientErr: assert.AnError}

	require.NotPanics(t, func() { d.Dump(t.Context(), dir) })
	assert.Empty(t, dumpedFiles(t, dir))
}

func TestNewKedaDumperWithoutRestConfigDoesNotPanic(t *testing.T) {
	t.Parallel()

	// kedaClient.NewForConfig dereferences its argument, and the dumpers are
	// built in a map literal, so a panic here would take the whole dump down
	// before any dumper runs.
	var d Resource
	require.NotPanics(t, func() { d = NewKedaDumper(cmd.Client{}, KedaScaledObject) })

	dir := t.TempDir()
	require.NotPanics(t, func() { d.Dump(t.Context(), dir) })
	assert.Empty(t, dumpedFiles(t, dir))
}

func TestKedaDumperListErrorBranches(t *testing.T) {
	t.Parallel()

	gr := schema.GroupResource{Group: "keda.sh", Resource: "scaledobjects"}

	// The two kedaAbsent branches are now observably different in the bundle
	// itself: a missing CRD leaves a marker naming the reason, a real failure
	// leaves nothing (it was warned about on the console instead). An empty
	// directory alone could not tell those apart, which is the whole point of
	// the marker. Neither branch may abort the dump.
	tests := []struct {
		name       string
		err        error
		wantMarker bool
	}{
		{"keda not installed leaves a marker", apierrors.NewNotFound(gr, "scaledobjects"), true},
		{"forbidden is a real error, not an absent CRD", apierrors.NewForbidden(gr, "scaledobjects", assert.AnError), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := kedafake.NewSimpleClientset()
			c.PrependReactor("list", "scaledobjects", func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, tc.err
			})

			dir := t.TempDir()
			d := KedaDumper{client: c, kedaType: KedaScaledObject}
			require.NotPanics(t, func() { d.Dump(t.Context(), dir) })

			files := dumpedFiles(t, dir)
			if !tc.wantMarker {
				assert.Empty(t, files, "a real error must not be recorded as an absent CRD")
				return
			}

			require.Equal(t, []string{"keda-not-installed.txt"}, files)
			content, err := os.ReadFile(filepath.Join(dir, files[0]))
			require.NoError(t, err)
			assert.Contains(t, string(content), KedaScaledObject,
				"the marker should name which kind was skipped")
		})
	}
}

func TestKedaDumperMasksTriggerMetadataCredentials(t *testing.T) {
	t.Parallel()

	so := &kedav1alpha1.ScaledObject{
		Name: "my-mqt", Namespace: "default", ResourceVersion: "1",
		OwnerReferences: []metav1.OwnerReference{mqtOwnerRef()},
		Spec: kedav1alpha1.ScaledObjectSpec{
			Triggers: []kedav1alpha1.ScaleTriggers{{
				Type: "rabbitmq",
				Metadata: map[string]string{
					"host":       "amqp://user:hunter2@rabbit:5672/",
					"queueName":  "orders",
					"queueLengt": "5",
				},
			}},
		},
	}

	dir := t.TempDir()
	d := KedaDumper{client: kedafake.NewSimpleClientset(so), kedaType: KedaScaledObject}
	d.Dump(t.Context(), dir)

	files := dumpedFiles(t, dir)
	require.Len(t, files, 1)
	content, err := os.ReadFile(filepath.Join(dir, files[0]))
	require.NoError(t, err)

	assert.NotContains(t, string(content), "hunter2",
		"an inline connection string must not reach a support bundle")
	assert.Contains(t, string(content), "queueName", "non-credential metadata stays diagnostic")
	assert.Contains(t, string(content), "orders")
}

func TestScaledObjectCleanLeavesTheListedObjectUnmutated(t *testing.T) {
	t.Parallel()

	// Spec.Triggers is a slice, so writing through it would edit the backing
	// array shared with the List result.
	so := &kedav1alpha1.ScaledObject{
		Spec: kedav1alpha1.ScaledObjectSpec{
			Triggers: []kedav1alpha1.ScaleTriggers{{
				Metadata: map[string]string{"password": "hunter2"},
			}},
		},
	}

	cleaned := scaledObjectClean(so)

	assert.Equal(t, "-", cleaned.Spec.Triggers[0].Metadata["password"])
	assert.Equal(t, "hunter2", so.Spec.Triggers[0].Metadata["password"],
		"the input object must not be modified in place")
}
