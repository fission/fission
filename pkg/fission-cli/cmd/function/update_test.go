// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package function

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fv1 "github.com/fission/fission/pkg/apis/core/v1"
)

// TestVersioningAutoHint guards the UX-review breadcrumb (F8): `fn update`
// on a function with versioning mode auto must point the caller at `fn
// versions` since the new version is minted asynchronously (once the build
// succeeds), not synchronously with the update that just printed.
func TestVersioningAutoHint(t *testing.T) {
	t.Run("no versioning config yields no hint", func(t *testing.T) {
		fn := &fv1.Function{ObjectMeta: metav1.ObjectMeta{Name: "hello"}}
		assert.Empty(t, versioningAutoHint(fn))
	})

	t.Run("manual mode yields no hint", func(t *testing.T) {
		fn := &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: "hello"},
			Spec:       fv1.FunctionSpec{Versioning: &fv1.VersioningConfig{Mode: fv1.VersioningModeManual}},
		}
		assert.Empty(t, versioningAutoHint(fn))
	})

	t.Run("auto mode names the function and the versions command", func(t *testing.T) {
		fn := &fv1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: "hello"},
			Spec:       fv1.FunctionSpec{Versioning: &fv1.VersioningConfig{Mode: fv1.VersioningModeAuto}},
		}
		hint := versioningAutoHint(fn)
		assert.Contains(t, hint, "versioning=auto")
		assert.Contains(t, hint, "fission fn versions --name hello")
	})
}
