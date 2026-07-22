// SPDX-FileCopyrightText: The Fission Authors
//
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// Validator is an interface that can be implemented to provide custom validation logic.
type Validator[T client.Object] interface {
	Validate(T) error
}

// Warner is an optional facet: a validator that also attaches non-fatal
// admission warnings (surfaced by kubectl/clients) for accepted-but-suspect
// specs, e.g. an annotation value that silently means "default".
type Warner[T client.Object] interface {
	Warnings(T) admission.Warnings
}

// Defaulter is an interface that can be implemented to provide custom defaulting logic.
type Defaulter[T client.Object] interface {
	ApplyDefaults(T) error
}

// UpdateValidator is an optional facet for rules that need the OLD object —
// immutability checks, transition rules. It runs on update in addition to
// Validator (which sees only the new object). The method is deliberately NOT
// named ValidateUpdate: implementers embed GenericWebhook, and a same-named
// method would shadow the admission.Validator implementation.
type UpdateValidator[T client.Object] interface {
	ValidateTransition(old, new T) error
}

// DeleteValidator is an optional facet that vetoes deletion (e.g. an object
// still referenced elsewhere). The method is deliberately NOT named
// ValidateDelete: implementers embed GenericWebhook, and a same-named method
// would shadow the admission.Validator implementation (embedded-struct
// registration defeats method shadowing the same way UpdateValidator does).
type DeleteValidator[T client.Object] interface {
	ValidateDeletion(ctx context.Context, obj T) error
}

// GenericWebhook implements the webhook interfaces for a generic type T.
type GenericWebhook[T client.Object] struct {
	Logger          logr.Logger
	Validator       Validator[T]
	Defaulter       Defaulter[T]
	Warner          Warner[T]
	UpdateValidator UpdateValidator[T]
	DeleteValidator DeleteValidator[T]
}

// SetupWebhookWithManager sets up the webhook with the manager.
func (w *GenericWebhook[T]) SetupWebhookWithManager(mgr ctrl.Manager, obj T) error {
	return ctrl.NewWebhookManagedBy(mgr, obj).
		WithDefaulter(w).
		WithValidator(w).
		Complete()
}

// Default implements admission.Defaulter.
func (w *GenericWebhook[T]) Default(_ context.Context, obj T) error {
	if w.Defaulter == nil {
		return nil
	}
	w.Logger.V(1).Info("default", "name", obj.GetName())
	return w.Defaulter.ApplyDefaults(obj)
}

// ValidateCreate implements admission.Validator.
func (w *GenericWebhook[T]) ValidateCreate(_ context.Context, obj T) (admission.Warnings, error) {
	w.Logger.V(1).Info("validate create", "name", obj.GetName())
	// Warnings are independent of Validator: a webhook may warn without
	// rejecting anything, and gating them on Validator would silently drop
	// the warnings such a webhook was written to emit.
	if w.Validator != nil {
		return w.warnings(obj), w.Validator.Validate(obj)
	}
	return w.warnings(obj), nil
}

// ValidateUpdate implements admission.Validator.
func (w *GenericWebhook[T]) ValidateUpdate(_ context.Context, oldObj, newObj T) (admission.Warnings, error) {
	w.Logger.V(1).Info("validate update", "name", newObj.GetName())
	if w.Validator != nil {
		if err := w.Validator.Validate(newObj); err != nil {
			return w.warnings(newObj), err
		}
	}
	if w.UpdateValidator != nil {
		if err := w.UpdateValidator.ValidateTransition(oldObj, newObj); err != nil {
			return w.warnings(newObj), err
		}
	}
	return w.warnings(newObj), nil
}

func (w *GenericWebhook[T]) warnings(obj T) admission.Warnings {
	if w.Warner == nil {
		return nil
	}
	return w.Warner.Warnings(obj)
}

// ValidateDelete implements admission.Validator.
func (w *GenericWebhook[T]) ValidateDelete(ctx context.Context, obj T) (admission.Warnings, error) {
	w.Logger.V(1).Info("validate delete", "name", obj.GetName())
	if w.DeleteValidator != nil {
		if err := w.DeleteValidator.ValidateDeletion(ctx, obj); err != nil {
			return nil, err
		}
	}
	return nil, nil
}
