// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package container

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	k8sErrs "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestDestroyOnCreateError guards the gate that keeps a transient fnCreate
// failure from tearing down a function's resources. A Conflict / AlreadyExists
// means the object exists and was concurrently modified (adopt racing a
// reconcile), so it must NOT be destroyed; genuine failures still warrant
// cleanup of a half-created new function.
func TestDestroyOnCreateError(t *testing.T) {
	t.Parallel()
	gr := schema.GroupResource{Group: "apps", Resource: "deployments"}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"conflict is transient — keep", k8sErrs.NewConflict(gr, "fn", errors.New("object has been modified")), false},
		{"already exists is transient — keep", k8sErrs.NewAlreadyExists(gr, "fn"), false},
		{"not found is genuine — clean up", k8sErrs.NewNotFound(gr, "fn"), true},
		{"forbidden is genuine — clean up", k8sErrs.NewForbidden(gr, "fn", errors.New("rbac")), true},
		{"opaque error is genuine — clean up", errors.New("quota exceeded"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, destroyOnCreateError(tc.err))
		})
	}
}
