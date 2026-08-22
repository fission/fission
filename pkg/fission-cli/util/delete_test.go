// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/driver/dummy"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

type fakeDeleter struct{ err error }

func (f fakeDeleter) Delete(_ context.Context, _ string, _ metav1.DeleteOptions) error {
	return f.err
}

func TestDeleteOne(t *testing.T) {
	t.Parallel()
	notFound := kerrors.NewNotFound(schema.GroupResource{Group: "fission.io", Resource: "functions"}, "fn")

	ignore := func() dummy.Cli {
		in := dummy.TestFlagSet()
		in.SetBool(flagkey.IgnoreNotFound, true)
		return in
	}

	t.Run("a successful delete reports deleted", func(t *testing.T) {
		t.Parallel()
		deleted, err := DeleteOne(dummy.TestFlagSet(), fakeDeleter{}, "fn", "function")
		require.NoError(t, err)
		assert.True(t, deleted)
	})

	t.Run("ignore-not-found swallows only a typed k8s NotFound", func(t *testing.T) {
		t.Parallel()
		deleted, err := DeleteOne(ignore(), fakeDeleter{err: notFound}, "fn", "function")
		require.NoError(t, err)
		assert.False(t, deleted, "nothing was deleted, so the caller must not print a success message")
	})

	// The regression this helper fixes: the previous string-suffix check
	// (util.IsNotFound) treated ANY error ending in "not found" — a
	// port-forward's "service <selector> not found", a proxied "Resource not
	// found" body — as the object being absent, so --ignore-not-found
	// reported success for failures that were not a missing object.
	t.Run("an unrelated error merely ending in 'not found' still fails", func(t *testing.T) {
		t.Parallel()
		unrelated := errors.New("service svc=router not found")
		_, err := DeleteOne(ignore(), fakeDeleter{err: unrelated}, "fn", "function")
		require.Error(t, err)
		assert.ErrorContains(t, err, "error deleting function")
		assert.ErrorIs(t, err, unrelated)
	})

	t.Run("without ignore-not-found even a typed NotFound is an error", func(t *testing.T) {
		t.Parallel()
		_, err := DeleteOne(dummy.TestFlagSet(), fakeDeleter{err: notFound}, "fn", "function")
		require.Error(t, err)
		assert.ErrorIs(t, err, notFound)
	})
}
