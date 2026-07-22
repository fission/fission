// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1 "github.com/fission/fission/pkg/apis/core/v1"
	"github.com/fission/fission/pkg/utils/loggerfactory"
)

// FunctionVersion is the admission webhook for v1.FunctionVersion (RFC-0025):
// it pins the spec immutable after creation and, on delete, vetoes removing a
// version that a FunctionAlias still points at.
type FunctionVersion struct {
	GenericWebhook[*v1.FunctionVersion]
	// reader is the uncached API reader used for the delete-guard's
	// FunctionAlias list and owning-Function lookup (get/list-only, no cache
	// warm-up needed), following the pkg/webhook/function.go pattern.
	reader client.Reader
}

func (r *FunctionVersion) SetupWebhookWithManager(mgr ctrl.Manager) error {
	r.Logger = loggerfactory.GetLogger().WithName("functionversion-resource")
	r.Validator = r
	r.UpdateValidator = r
	r.DeleteValidator = r
	r.reader = mgr.GetAPIReader()
	return r.GenericWebhook.SetupWebhookWithManager(mgr, &v1.FunctionVersion{})
}

// user change verbs to add "create" if creation-time defaulting/validation
// beyond the CEL/schema rules is ever needed here; the webhook rule below
// registers only update;delete, so ValidateCreate never fires for this
// resource today — create-time enforcement is CEL/schema-only.
//
//+kubebuilder:webhook:path=/validate-fission-io-v1-functionversion,mutating=false,failurePolicy=fail,sideEffects=None,groups=fission.io,resources=functionversions,verbs=update;delete,versions=v1,name=vfunctionversion.fission.io,admissionReviewVersions=v1

var _ admission.Validator[*v1.FunctionVersion] = &FunctionVersion{}

func (r *FunctionVersion) Validate(new *v1.FunctionVersion) error {
	if err := new.Validate(); err != nil {
		return v1.AggregateValidationErrors("FunctionVersion", err)
	}
	return nil
}

// ValidateTransition pins the whole spec after creation: a FunctionVersion is
// an immutable snapshot of a Function at publish time (RFC-0025) — mutating
// it after the fact would silently invalidate every consumer that treats the
// name as a content-addressed pointer (alias resolution, GC, rollback audit).
// Publish a new version instead of editing this one.
func (r *FunctionVersion) ValidateTransition(old, new *v1.FunctionVersion) error {
	if !equality.Semantic.DeepEqual(old.Spec, new.Spec) {
		return v1.AggregateValidationErrors("FunctionVersion",
			v1.MakeValidationErr(v1.ErrorInvalidValue, "FunctionVersion.Spec", new.Spec.FunctionName,
				"a published FunctionVersion is immutable; publish a new version instead"))
	}
	return nil
}

var _ DeleteValidator[*v1.FunctionVersion] = &FunctionVersion{}

// ValidateDeletion rejects deleting a FunctionVersion that any FunctionAlias
// in the same namespace still references — via spec.Version,
// spec.SecondaryVersion, or status.ResolvedVersion — UNLESS the owning
// Function (this version's ownerRef with Kind=Function) is already gone or is
// itself being deleted: k8s garbage-collects a Function's versions and
// aliases in unspecified order, and both carry an ownerRef back to the
// Function, so blocking on a sibling that is about to be GC'd anyway would
// just make the cascade delete fail.
//
// Scope honesty (RFC-0025 L246): this is defense-in-depth for the
// GC-vs-alias-create admission race only — two in-flight admissions are
// unordered, so this guard does NOT close that race; phase-4 retention GC
// must still re-List aliases immediately before each delete (the
// aliasgc.tla recheck-inside-delete) and treat this webhook as a second net,
// not the guard. That framing does not extend to reader errors: a failed
// alias List below means the guard cannot confirm safety, so it fails
// closed rather than silently allowing a possibly-referenced delete.
func (r *FunctionVersion) ValidateDeletion(ctx context.Context, fv *v1.FunctionVersion) error {
	if r.reader == nil {
		return nil
	}

	if ownerName, ok := ownerFunctionRef(fv); ok {
		var fn v1.Function
		err := r.reader.Get(ctx, types.NamespacedName{Name: ownerName, Namespace: fv.Namespace}, &fn)
		switch {
		case apierrors.IsNotFound(err):
			// Owning Function is already gone: cascade-delete escape.
			return nil
		case err != nil:
			// Transient read failure: fall through to the reference check
			// below rather than guessing at the owner's state.
			r.Logger.V(1).Info("could not read owning Function for delete guard; falling back to reference check", "functionVersion", fv.Name, "function", ownerName, "error", err)
		case fn.DeletionTimestamp != nil:
			// Owning Function is itself being deleted: cascade-delete escape.
			return nil
		}
	}

	var aliases v1.FunctionAliasList
	if err := r.reader.List(ctx, &aliases, client.InNamespace(fv.Namespace)); err != nil {
		// Fail closed: unlike the GC-vs-alias-create race this guard
		// deliberately doesn't claim to close (see scope-honesty note above),
		// a failed List means we cannot confirm this version is unreferenced
		// at all, so the safe default is to reject the delete and let the
		// caller retry rather than silently allow a possibly-referenced one
		// through.
		return fmt.Errorf("could not list FunctionAliases in namespace %q to verify FunctionVersion %q is unreferenced: %w", fv.Namespace, fv.Name, err)
	}

	for _, a := range aliases.Items {
		if a.Spec.Version == fv.Name || a.Spec.SecondaryVersion == fv.Name || a.Status.ResolvedVersion == fv.Name {
			return fmt.Errorf("FunctionVersion %q is still referenced by FunctionAlias %q; repoint or delete the alias first", fv.Name, a.Name)
		}
	}
	return nil
}

// ownerFunctionRef returns the name of the FunctionVersion's ownerRef with
// Kind=Function, if any.
func ownerFunctionRef(fv *v1.FunctionVersion) (name string, ok bool) {
	for _, or := range fv.OwnerReferences {
		if or.Kind == "Function" {
			return or.Name, true
		}
	}
	return "", false
}
