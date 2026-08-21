// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"fmt"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fission/fission/pkg/fission-cli/cliwrapper/cli"
	flagkey "github.com/fission/fission/pkg/fission-cli/flag/key"
)

// deleter is the one typed-clientset method DeleteOne needs; every generated
// resource client (Functions(ns), TimeTriggers(ns), ...) satisfies it. Delete
// erases the resource type entirely, so no type parameter is required.
type deleter interface {
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
}

// DeleteOne is the shared core of a `fission <resource> delete` subcommand:
// delete name via client, honoring --ignore-not-found. It reports
// deleted=false with a nil error when the object was already gone and the
// caller opted to ignore that, so the caller skips its success message; any
// other failure comes back wrapped as "error deleting <errNoun>: ...".
//
// Not-found is detected with the typed kerrors.IsNotFound check. The string
// suffix match some callers used before (a now-deleted util.IsNotFound) also
// matched unrelated errors that merely end in "not found" — e.g. a
// port-forward failing with `service <selector> not found` — making
// --ignore-not-found report success for failures that were not the object
// being absent.
func DeleteOne(input cli.Input, client deleter, name, errNoun string) (deleted bool, err error) {
	err = client.Delete(input.Context(), name, metav1.DeleteOptions{})
	if err != nil {
		if input.Bool(flagkey.IgnoreNotFound) && kerrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("error deleting %s: %w", errNoun, err)
	}
	return true, nil
}
