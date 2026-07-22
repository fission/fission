// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// FunctionAlias is the admission webhook for v1.FunctionAlias (RFC-0025): on
// top of the type's own field-shape rules, it checks that a name-pinned
// target (spec.version / spec.secondaryVersion) actually exists and belongs
// to the alias's own function.
type FunctionAlias struct {
	GenericWebhook[*v1.FunctionAlias]
	// reader is the uncached API reader used to look up the referenced
	// FunctionVersion(s) (get-only, so no cache warm-up or list/watch RBAC
	// needed), following the pkg/webhook/function.go pattern.
	reader client.Reader
}

func (r *FunctionAlias) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = loggerfactory.GetLogger().WithName("functionalias-resource")
	r.Validator = r
	r.reader = mgr.GetAPIReader()
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.FunctionAlias{})
}

//+kubebuilder:webhook:path=/validate-fission-io-v1-functionalias,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=functionaliases,verbs=create;update,versions=v1,name=vfunctionalias.fission.io,admissionReviewVersions=v1

var _ admission.Validator[*v1.FunctionAlias] = &FunctionAlias{}

func (r *FunctionAlias) Validate(new *v1.FunctionAlias) error {
	if err := new.Validate(); err != nil {
		return v1.AggregateValidationErrors("FunctionAlias", err)
	}

	if err := r.validateVersionRef(context.Background(), new, new.Spec.Version, "FunctionAliasSpec.Version"); err != nil {
		return v1.AggregateValidationErrors("FunctionAlias", err)
	}
	if err := r.validateVersionRef(context.Background(), new, new.Spec.SecondaryVersion, "FunctionAliasSpec.SecondaryVersion"); err != nil {
		return v1.AggregateValidationErrors("FunctionAlias", err)
	}
	return nil
}

// validateVersionRef checks a name-pinned target: the FunctionVersion must
// exist and its spec.functionName must match the alias's own
// spec.functionName (an alias for one function cannot resolve to a version
// snapshot of a different one). A digest-pinned primary target
// (spec.packageDigest set, spec.version empty) is exempt — that resolution
// happens asynchronously and is eventually consistent, so there is nothing to
// look up here. Fails open on a transient read error: the authoritative
// check for a dangling reference is alias-resolution at reconcile time, same
// reasoning as the Workflow webhook's referenced-function check.
func (r *FunctionAlias) validateVersionRef(ctx context.Context, fa *v1.FunctionAlias, versionName, field string) error {
	if versionName == "" || r.reader == nil {
		return nil
	}

	var fv v1.FunctionVersion
	err := r.reader.Get(ctx, types.NamespacedName{Name: versionName, Namespace: fa.Namespace}, &fv)
	switch {
	case apierrors.IsNotFound(err):
		return fmt.Errorf("%s %q does not exist in namespace %q", field, versionName, fa.Namespace)
	case err != nil:
		r.Logger.V(1).Info("could not read referenced FunctionVersion; deferring to alias-resolution enforcement", "functionAlias", fa.Name, "version", versionName, "error", err)
		return nil
	case fv.Spec.FunctionName != fa.Spec.FunctionName:
		return fmt.Errorf("%s %q belongs to function %q, not %q", field, versionName, fv.Spec.FunctionName, fa.Spec.FunctionName)
	}
	return nil
}
